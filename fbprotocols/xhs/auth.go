package xhs

// Session credential handling for the xhs (xiaohongshu) web API.
//
// A single cookie jar (a1 / webId / web_session / websectiga ...) is shared by
// every endpoint; it persists to ~/.config/fedlet/xhs-auth.json (0600). The
// visitor cookies a1 + webId are generated fresh and feed the signature; a
// guest web_session can be obtained anonymously via login/activate, and a real
// one via the QR or phone+code routes of the web login API. Both the hot list
// and the notification feeds run under that jar.
//
// Endpoint details are cross-checks against xhshow-0.2.0 and the actively
// maintained xiaohongshu-cli / Spider_XHS projects are marked 待实测 (to be
// verified against the live site) where the exact field layout was not
// individually confirmed.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrNotLoggedIn is returned when an authenticated endpoint rejects the stored
// session. It triggers the expired-session handler but is not a network or
// rate-limit failure.
var ErrNotLoggedIn = errors.New("xhs: not logged in or session expired")

// ErrRiskControl is returned when a request is blocked with a risk challenge
// (HTTP 461/471/472) whose captcha could not be cleared interactively.
var ErrRiskControl = errors.New("xhs: blocked by risk control")

// Auth status values reported by AuthStatus().
const (
	AuthStatusEmpty   = ""
	AuthStatusReady   = "ready"
	AuthStatusInvalid = "invalid"
)

// auth user/me identity envelope (GET /api/sns/web/v2/user/me).
type meEnvelope struct {
	Nickname   string `json:"nickname"`
	UserID     string `json:"user_id"`
	Guest      bool   `json:"guest"`
	IPLocation string `json:"ip_location"`
}

const (
	activateURL    = "/api/sns/web/v1/login/activate"
	qrCreateURL    = "/api/sns/web/v1/login/qrcode/create"
	qrStatusURL    = "/api/sns/web/v1/login/qrcode/status"
	qrUserInfoURL  = "/api/qrcode/userinfo"
	sendCodeURL    = "/api/sns/web/v2/login/send_code"
	checkCodeURL   = "/api/sns/web/v1/login/check_code"
	loginCodeURL   = "/api/sns/web/v2/login/code"
	loginStatusURL = "/api/sns/web/v2/user/me"

	// webSessionSecCookie mirrors the web_session_sec login cookie; it is
	// persisted alongside the main session and cleared on every relogin.
	webSessionSecCookie = "web_session_sec"
)

// authFileJSON is the persisted credential shape in xhs-auth.json.
type authFileJSON struct {
	Cookies     map[string]string `json:"cookies"`
	User        string            `json:"user,omitempty"`
	Status      string            `json:"status"`
	LoginMethod string            `json:"login_method,omitempty"`
}

var (
	authMu      sync.Mutex
	lastAuthErr error
)

// session is the shared client: cookie jar + signed transport. It is created
// lazily (fresh visitor cookies) and replaced incrementally by the login UI.
var session *xhsClient

// sessionMu guards the session pointer itself.
var sessionMu sync.Mutex

// newSession builds a fresh client with generated visitor cookies. If a
// persisted jar exists it is loaded instead so the a1/webId stay stable.
func newSession() *xhsClient {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	if session != nil {
		return session
	}
	c := newXHSClient(clientOptions{})
	loadAuthInto(c)
	session = c
	return c
}

// client returns the shared session, creating it on first use.
func client() *xhsClient {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	if session == nil {
		session = newXHSClient(clientOptions{})
		loadAuthInto(session)
	}
	return session
}

// AuthStatus returns the current credential state: empty/ready/invalid.
func AuthStatus() string {
	c := client()
	if c.cookies.get(xhsWebSessionName) == "" {
		return AuthStatusEmpty
	}
	a := c.opts.auth
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

// AuthUser returns the logged-in account nickname, if known from a verify.
func AuthUser() string {
	c := client()
	a := c.opts.auth
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.user
}

// LastAuthErr returns the last auth failure (nil when none/ok).
func LastAuthErr() error {
	authMu.Lock()
	defer authMu.Unlock()
	return lastAuthErr
}

// httpStatusRisk and the risk set the api uses for captcha challenges.
const (
	httpStatusRisk  = 461
	authStatusGuest = "guest"
)

// isHttpAuthErr flags statuses/signals that mean "session rejected/expired"
// rather than a transient failure.
func isHttpAuthErr(err error, status int) bool {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return true
	}
	return errors.Is(err, ErrNotLoggedIn) || isAuthRejectedText(err)
}

// isAuthRejectedText flags error texts that mean "session rejected/expired"
// (the API answers -100 / 未登录 inside HTTP 200 envelopes).
func isAuthRejectedText(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "未登录") || strings.Contains(s, "-100") ||
		strings.Contains(s, "auth") || strings.Contains(s, "http 401") || strings.Contains(s, "http 403")
}

// authStatusInvalid marks the stored session as rejected.
func authStatusInvalid() {
	c := client()
	a := c.opts.auth
	a.mu.Lock()
	a.status = AuthStatusInvalid
	a.mu.Unlock()
}

func setAuthStatusReady() {
	c := client()
	a := c.opts.auth
	a.mu.Lock()
	a.status = AuthStatusReady
	a.mu.Unlock()
}

// markSessionInvalid records the rejection reason and flips the auth status.
// The dead web_session cookie is dropped so it stops being replayed; the
// rejection itself is not persisted to the auth file (a restart reloads the
// last-known jar and re-verifies).
func markSessionInvalid(err error) {
	authMu.Lock()
	lastAuthErr = err
	authMu.Unlock()
	authStatusInvalid()
	client().cookies.del(xhsWebSessionName)
}

func clearAuthErr() {
	authMu.Lock()
	lastAuthErr = nil
	authMu.Unlock()
}

// verifySession checks the stored session against /user/me and refreshes the
// user name and AuthStatus. It returns ErrNotLoggedIn when the session is
// rejected (or missing), which the caller should treat as expired and trigger
// re-login. A guest web_session answers with guest=true and counts as "not
// logged in" for the auth gateway (feeds still work anonymously).
func verifySession() error {
	c := client()
	if c.cookies.get(xhsWebSessionName) == "" {
		markSessionInvalid(ErrNotLoggedIn)
		return ErrNotLoggedIn
	}
	out, status, err := c.getJSON(loginStatusURL, nil)
	if err != nil {
		if status == httpStatusRisk {
			// A captcha challenge is not a session failure: keep the current
			// status (the client-level risk resolver deals with the challenge).
			return ErrRiskControl
		}
		if isHttpAuthErr(err, status) {
			markSessionInvalid(ErrNotLoggedIn)
			return ErrNotLoggedIn
		}
		return fmt.Errorf("xhs: verify session: %w", err)
	}
	me := meEnvelope{}
	if b, err := json.Marshal(out); err == nil {
		_ = json.Unmarshal(b, &me)
	}
	if me.Nickname == "" {
		me.Nickname = shortID(me.UserID)
	}
	a := c.opts.auth
	a.mu.Lock()
	a.user = me.Nickname
	a.status = AuthStatusReady
	if me.Guest {
		a.status = authStatusGuest
	}
	a.mu.Unlock()
	clearAuthErr()
	if me.Nickname != "" {
		log.Printf("xhs: session ok for %q", me.Nickname)
	}
	saveAuth()
	return nil
}

// ---- QR login (official web flow, verified 2026-09) ----
//
// POST /api/sns/web/v1/login/qrcode/create {"qr_type":1} -> qr_id/code/url;
// then poll POST /api/qrcode/userinfo {"qrId","code"} until codeStatus==2
// (0 waiting, 1 scanned, 2 confirmed, 3 expired); finally the session comes
// from GET /api/sns/web/v1/login/qrcode/status with the qr_id/code params.
// (The poll and completion calls are also the ones the web client makes.)

// loginQRCode holds the fields of a fresh QR login request.
type loginQRCode struct {
	QRID string `json:"qr_id"`
	Code string `json:"code"`
	URL  string `json:"url"`
}

// qrCreate starts a new QR login and returns the qr_id/code/url triplets.
func qrCreate() (*loginQRCode, error) {
	out, _, err := client().postJSON(qrCreateURL, []kvParam{{key: "qr_type", val: "1"}})
	if err != nil {
		return nil, fmt.Errorf("xhs: qr create: %w", err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("xhs: qr create marshal: %w", err)
	}
	var q loginQRCode
	if err := json.Unmarshal(b, &q); err != nil {
		return nil, fmt.Errorf("xhs: qr create parse: %w", err)
	}
	if q.QRID == "" || q.Code == "" || q.URL == "" {
		return nil, errors.New("xhs: qr create returned incomplete data")
	}
	return &q, nil
}

// qrPollStatus polls once and reports the codeStatus (0/1/2/3) and the
// confirmed userId (present only when codeStatus==2).
func qrPollStatus(q *loginQRCode) (status int, userID string, err error) {
	out, _, err := client().postJSON(qrUserInfoURL, []kvParam{{key: "qrId", val: q.QRID}, {key: "code", val: q.Code}})
	if err != nil {
		return 0, "", fmt.Errorf("xhs: qr poll: %w", err)
	}
	st64, _ := numAsInt(out["codeStatus"])
	if id, ok := out["userId"].(string); ok {
		userID = id
	}
	if id, ok := out["user_id"].(string); ok {
		userID = id
	}
	return int(st64), userID, nil
}

// qrComplete fetches the session once the poll reports a confirmed login and
// folds login_info.session (and any Set-Cookie) into the shared jar.
func qrComplete(q *loginQRCode, confirmedUserID string) error {
	out, status, err := client().getJSON(qrStatusURL, []kvParam{{key: "qr_id", val: q.QRID}, {key: "code", val: q.Code}})
	if err != nil {
		return fmt.Errorf("xhs: qr complete: %w", err)
	}
	if status != 200 {
		return fmt.Errorf("xhs: qr complete http %d", status)
	}
	if cs, _ := numAsInt(out["code_status"]); cs != 2 {
		return fmt.Errorf("xhs: qr complete status %v", out["code_status"])
	}
	session, _ := out["session"].(string)
	for _, key := range []string{"session", "web_session"} {
		if v, ok := out[key].(string); ok && v != "" {
			session = v
		}
	}
	if li, ok := out["login_info"].(map[string]any); ok {
		if s, ok := li["session"].(string); ok && s != "" {
			session = s
		}
		if id, ok := li["user_id"].(string); ok && id != "" {
			c := client()
			a := c.opts.auth
			a.mu.Lock()
			if a.user == "" {
				a.user = id
			}
			a.mu.Unlock()
		}
	}
	if session == "" {
		return errors.New("xhs: qr complete returned no session")
	}
	client().cookies.set(xhsWebSessionName, session)
	foldSecureSession(out)
	return nil
}

// foldSecureSession persists the login's secure_session when present and
// otherwise clears it, so a stale one never lingers after a relogin.
func foldSecureSession(out map[string]any) {
	sec := ""
	if li, ok := out["login_info"].(map[string]any); ok {
		if s, ok := li["secure_session"].(string); ok && s != "" {
			sec = s
		}
	}
	if sec == "" {
		if s, ok := out["secure_session"].(string); ok {
			sec = s
		}
	}
	client().cookies.set(webSessionSecCookie, sec)
}

// ---- phone + verify code login (official web flow, verified 2026-09) ----
//
// GET /api/sns/web/v2/login/send_code (phone/zone/type) mails the code; the
// code is then exchanged for a mobile_token via GET /api/sns/web/v1/login/
// check_code and finally POST /api/sns/web/v2/login/code
// {"mobile_token","zone","phone"} returns the session.

// phoneSendCode requests an SMS code to be sent to a mobile number.
func phoneSendCode(phone string, zone string) error {
	if phone == "" {
		return errors.New("xhs: empty phone")
	}
	if zone == "" {
		zone = "86"
	}
	_, status, err := client().getJSON(sendCodeURL, []kvParam{
		{key: "phone", val: phone},
		{key: "zone", val: zone},
		{key: "type", val: "login"},
	})
	if err != nil {
		if isHttpAuthErr(err, status) {
			return nil // 401-ish means "code throttled/flooded", treated as transient
		}
		return fmt.Errorf("xhs: send code: %w", err)
	}
	return nil
}

// phoneCheckCode exchanges the SMS code for a one-time mobile_token.
func phoneCheckCode(phone, code, zone string) (string, error) {
	if zone == "" {
		zone = "86"
	}
	out, _, err := client().getJSON(checkCodeURL, []kvParam{
		{key: "phone", val: phone},
		{key: "zone", val: zone},
		{key: "code", val: code},
	})
	if err != nil {
		return "", fmt.Errorf("xhs: check code: %w", err)
	}
	tok, _ := out["mobile_token"].(string)
	if tok == "" {
		return "", errors.New("xhs: check code returned no mobile_token")
	}
	return tok, nil
}

// phoneLogin exchanges the phone + SMS code for a web_session cookie.
func phoneLogin(phone, code, zone string) error {
	// Single user-visible step: check the code, then finish the login.
	tok, err := phoneCheckCode(phone, code, zone)
	if err != nil {
		return err
	}
	out, status, err := client().postJSON(loginCodeURL, []kvParam{
		{key: "mobile_token", val: tok},
		{key: "zone", val: zone},
		{key: "phone", val: phone},
	})
	if err != nil {
		if isHttpAuthErr(err, status) {
			return fmt.Errorf("xhs: login code rejected (wrong code?): %v", err)
		}
		return fmt.Errorf("xhs: login code: %w", err)
	}
	sess, _ := out["session"].(string)
	if sess == "" {
		if li, ok := out["login_info"].(map[string]any); ok {
			if s, ok := li["session"].(string); ok {
				sess = s
			}
		}
	}
	if sess == "" {
		return errors.New("xhs: login code returned no session")
	}
	client().cookies.set(xhsWebSessionName, sess)
	foldSecureSession(out)
	return nil
}

// ---- guest session (anonymous) ----

// ensureGuestSession mints a guest web_session via login/activate so the hot
// feed can run without a real account. Fails non-destructively (the jar keeps
// its visitor cookies which are enough for most endpoints).
func ensureGuestSession() {
	c := client()
	if c.cookies.get(xhsWebSessionName) != "" {
		return
	}
	out, _, err := c.postJSON(activateURL, nil)
	if err != nil {
		log.Printf("xhs: guest activate failed: %v", err)
		return
	}
	if s, ok := out["session"].(string); ok && s != "" {
		c.cookies.set(xhsWebSessionName, s)
		foldSecureSession(out)
		log.Printf("xhs: guest session ok")
		c.opts.auth.mu.Lock()
		c.opts.auth.status = authStatusGuest
		c.opts.auth.mu.Unlock()
		saveAuth()
		return
	}
	log.Printf("xhs: guest activate returned no session (data=%v)", keysOf(out))
}

// ---- session persistence ----

func authFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "fedlet", "xhs-auth.json")
}

func loadAuthInto(c *xhsClient) {
	data, err := os.ReadFile(authFilePath())
	if err != nil {
		return
	}
	var f authFileJSON
	if err := json.Unmarshal(data, &f); err != nil {
		log.Printf("xhs: load auth parse error: %v", err)
		return
	}
	if len(f.Cookies) > 0 {
		c.cookies.load(f.Cookies)
		c.syncFromJar()
	}
	a := c.opts.auth
	a.mu.Lock()
	a.user = f.User
	a.loginMethod = f.LoginMethod
	if f.Status == AuthStatusInvalid {
		a.status = AuthStatusInvalid
	} else if c.cookies.get(xhsWebSessionName) != "" {
		a.status = AuthStatusReady
	} else {
		a.status = AuthStatusEmpty
	}
	a.mu.Unlock()
}

func saveAuth() {
	c := client()
	f := authFileJSON{
		Cookies: c.cookies.all(),
	}
	a := c.opts.auth
	a.mu.Lock()
	f.User = a.user
	f.Status = a.status
	f.LoginMethod = a.loginMethod
	a.mu.Unlock()

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		log.Printf("xhs: save auth marshal error: %v", err)
		return
	}
	p := authFilePath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		log.Printf("xhs: save auth mkdir error: %v", err)
		return
	}
	if err := os.WriteFile(p, data, 0600); err != nil {
		log.Printf("xhs: save auth write error: %v", err)
	}
}

// ---- small helpers ----

func numAsInt(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, true
		}
	case string:
		var i int64
		if _, err := fmt.Sscanf(n, "%d", &i); err == nil {
			return i, true
		}
	}
	return 0, false
}

// shortID is a compact display fallback for the user id.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:4] + "…" + id[len(id)-4:]
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
