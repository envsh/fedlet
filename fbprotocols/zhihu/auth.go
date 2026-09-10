package zhihu

// Session credential handling for the Zhihu web API.
//
// Authentication mirrors the zhihu-plus-plus (zly2006/zhihu-plus-plus, AGPL-3)
// flows: the QR scan of the official web ceremony exchanges for a z_c0 cookie,
// and the phone + verify-code route speaks the Android account protocol on
// api.zhihu.com (encrypted bodies, see phonelogin.go) whose returned cookie
// map doubles as the web session. Either way the result is a z_c0 session
// cookie persisted to ~/.config/fedlet/zhihu-auth.json (0600). Login
// endpoints are NOT signed (no x-zse-93/96 for the web login ceremony) and NOT
// browser-ID (DU) gated; the only requirement is the exact fetch-style header
// set + a real _xsrf from the /signin bootstrap (see client.go loginFlowHeaders
// / warmup). Both the hot list and notification feeds run under the main z_c0
// session.
//
// Endpoint layout was cross-checked against the live zhihu-plus-plus on
// 2026-09-10 (QR begin -> 200 token/link, scan_info polls with x-zse-93;
// phone digits -> /api/account/prod/auth/digits, sign-in -> /sign_in).

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
	"time"
)

// Auth status values reported by AuthStatus().
const (
	AuthStatusEmpty   = ""
	AuthStatusReady   = "ready"
	AuthStatusInvalid = "invalid"
)

const (
	meURL     = "https://www.zhihu.com/api/v4/me"
	qrBaseURL = "https://www.zhihu.com/api/v3/account/api/login/qrcode"
	// sendCodeURL / mobileLoginURL are the legacy (pre-2026) phone login
	// endpoints. The 2026 web replaced them with the oauth/validate/sign_in/
	// digits + oauth/sign_in/digits routes gated behind the encrypted-body +
	// browser-ID wrapper; the supported phone route is the Android account
	// protocol in phonelogin.go. The URLs are kept only as documentation.
	sendCodeURL    = "https://www.zhihu.com/api/v3/account/api/send_verify_code" // legacy, HTTP 404 since 2026
	mobileLoginURL = "https://www.zhihu.com/api/v3/account/api/login"            // legacy, superseded by oauth flow
)

// authFileJSON is the persisted credential shape in zhihu-auth.json.
type authFileJSON struct {
	DC0    string `json:"d_c0"`
	ZC0    string `json:"z_c0,omitempty"`
	XSRF   string `json:"_xsrf,omitempty"`
	User   string `json:"user,omitempty"`
	Status string `json:"status"`
}

var (
	authMu      sync.Mutex
	lastAuthErr error
)

// AuthStatus returns the current credential state: empty/ready/invalid.
func AuthStatus() string {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	return sess.status
}

// AuthUser returns the logged-in account name, if known from a verify.
func AuthUser() string {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	return sess.user
}

// LastAuthErr returns the last auth failure (nil when none/ok).
func LastAuthErr() error {
	authMu.Lock()
	defer authMu.Unlock()
	return lastAuthErr
}

func setAuthStatus(s string) {
	sess.mu.Lock()
	sess.status = s
	sess.mu.Unlock()
	if s == AuthStatusInvalid {
		saveAuth()
	}
}

func setAuthUser(u string) {
	sess.mu.Lock()
	sess.user = u
	sess.mu.Unlock()
}

func markSessionInvalid(err error) {
	authMu.Lock()
	lastAuthErr = err
	authMu.Unlock()
	setAuthStatus(AuthStatusInvalid)
}

func clearAuthErr() {
	authMu.Lock()
	lastAuthErr = nil
	authMu.Unlock()
}

// meResp is the /api/v4/me identity envelope.
type meResp struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// verifySession checks the stored z_c0 against /api/v4/me and refreshes the
// user name and AuthStatus. It returns ErrNotLoggedIn when the session is
// rejected, which the caller should treat as expired and trigger re-login.
func verifySession() error {
	start := time.Now()
	if !sess.hasZ() {
		markSessionInvalid(errors.New("zhihu: no session (z_c0 missing)"))
		logf("zhihu: verify session: no z_c0 (elapsed=%s)", time.Since(start).Round(time.Millisecond))
		return ErrNotLoggedIn
	}
	var me meResp
	if err := getJSON(&me, meURL, true); err != nil {
		if errors.Is(err, ErrNotLoggedIn) {
			markSessionInvalid(err)
			logf("zhihu: verify session: not logged in (elapsed=%s)", time.Since(start).Round(time.Millisecond))
			return err
		}
		if errors.Is(err, errUnverifiable) {
			// /api/v4/me answered an HTML page: the session cannot be verified,
			// so it is treated as expired and routed to the login gateway.
			markSessionInvalid(err)
			logf("zhihu: verify session: unverifiable html (elapsed=%s)", time.Since(start).Round(time.Millisecond))
			return ErrNotLoggedIn
		}
		logf("zhihu: verify session: %v (elapsed=%s)", err, time.Since(start).Round(time.Millisecond))
		return fmt.Errorf("zhihu: verify session: %w", err)
	}
	logf("zhihu: verify session: ok user=%s elapsed=%s", me.Name, time.Since(start).Round(time.Millisecond))
	setAuthUser(me.Name)
	clearAuthErr()
	setAuthStatus(AuthStatusReady)
	if me.Name != "" {
		log.Printf("zhihu: session ok for %q", me.Name)
	}
	return nil
}

// ---- QR login (zhihu-plus-plus parity, official flow) ----
//
// The web login uses POST /qrcode to obtain the token and GET
// /qrcode/{token}/scan_info for status polling (GET on /qrcode answers 405;
// /scan + /confirm are the legacy/non-official endpoints). These endpoints are
// NOT signed (no x-zse-93/96) and NOT browser-ID gated; they only need a real
// _xsrf + d_c0 from the /signin + /udid bootstrap and the fetch-style headers
// (see loginFlowHeaders). scan_info status contract: 0 = not scanned, 1 =
// scanned/awaiting confirm; success is signalled by access_token / user_id in
// the body, a z_c0 cookie value inside the body ("cookie"/"cookies"/"z_c0"
// fields) or a Set-Cookie z_c0. HTTP 403 code 40352 (need_login) is the
// network risk-control gate: an /account/unhuman URL is returned for a human
// to verify.

type qrBeginResp struct {
	Code        int    `json:"code"`
	Token       string `json:"token"`
	QrcodeToken string `json:"qrcode_token"`
	URL         string `json:"url"`
	Link        string `json:"link"`
	ExpiresAt   int64  `json:"expires_at"`
	Expires     int    `json:"expires"`
}

// parseQrBegin decodes a qrBegin POST response and returns the scan token,
// the QR link and the expiry (epoch or TTL, see normalizeQrDeadline).
func parseQrBegin(body []byte) (token, link string, expiresAt int64, err error) {
	var r qrBeginResp
	if err := json.Unmarshal(body, &r); err != nil {
		return "", "", 0, fmt.Errorf("zhihu: qr begin parse: %w", err)
	}
	token = r.Token
	if token == "" {
		token = r.QrcodeToken
	}
	link = r.URL
	if link == "" {
		link = r.Link
	}
	if token == "" {
		return "", "", 0, errors.New("zhihu: qr begin returned no token")
	}
	return token, link, r.ExpiresAt, nil
}

// qrBegin obtains a new scan token and the display URL from the official
// endpoint using the browser-parity login ceremony (no signature headers).
func qrBegin() (token, url string, expiresAt int64, err error) {
	if !refreshLoginContext() {
		st, riskURL := sess.qrGateStatus()
		log.Printf("zhihu: qr begin skipped: qr_gate=http %d risk_url=%s cookies=%s", st, riskURL, sess.cookieState())
		return "", "", 0, fmt.Errorf("知乎当前要求网络/人机验证(HTTP %d),请先完成验证后再试,或用「手机号+验证码」登录", st)
	}
	body, status, err := loginRequest(http.MethodPost, qrBaseURL, []byte("{}"), signinReferer, false)
	if err != nil {
		return "", "", 0, fmt.Errorf("zhihu: qr begin: %w", err)
	}
	if status != http.StatusOK {
		log.Printf("zhihu: qr begin http %d: cookies=%s body=%s", status, sess.cookieState(), truncate(string(body), 200))
		return "", "", 0, fmt.Errorf("二维码获取失败(HTTP %d):知乎未认可本次登录票据,请稍后刷新重试,或改用「手机号+验证码」登录", status)
	}
	token, url, expiresAt, err = parseQrBegin(body)
	if err != nil {
		return "", "", 0, fmt.Errorf("zhihu: %w", err)
	}
	return token, url, expiresAt, nil
}

// qrScanInfo polls the official scan_info endpoint once with the polling
// headers (accept */* + sec-fetch* + x-zse-93).
func qrScanInfo(token string) ([]byte, int, error) {
	if token == "" {
		return nil, 0, errors.New("zhihu: qr scan_info: empty token")
	}
	return loginRequest(http.MethodGet, qrBaseURL+"/"+token+"/scan_info", nil, zhihuSigninURL, true)
}

// scanInfoRiskControl reports whether the scan_info poll hit the network
// risk-control gate (HTTP 403, code 40352 / need_login). When ok, redirect is
// the /account/unhuman verification URL the human must complete.
func scanInfoRiskControl(body []byte) (redirect string, ok bool) {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return "", false
	}
	code, _ := m["code"].(float64)
	e, hasErr := m["error"].(map[string]any)
	if hasErr {
		if n, _ := e["code"].(float64); n != 0 {
			code = n
		}
		if nl, _ := e["need_login"].(bool); nl {
			if rd, _ := e["redirect"].(string); rd != "" {
				return rd, true
			}
		}
	}
	if code == 40352 {
		var rd string
		if hasErr {
			rd, _ = e["redirect"].(string)
		}
		if s, _ := m["redirect"].(string); rd == "" {
			rd = s
		}
		return rd, true
	}
	return "", false
}

// parseScanInfo interprets a single scan_info poll response. done reports a
// confirmed login, scanned reports "scanned but not confirmed", zc0 is any
// z_c0 cookie value embedded in the body, failed carries a detected error.
func parseScanInfo(body []byte) (done bool, scanned bool, zc0 string, failed string) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return false, false, "", ""
	}
	// Explicit error payloads (code != 0 / error.message) fail fast instead of
	// waiting out the poll deadline.
	if code, _ := m["code"].(float64); code != 0 {
		return false, false, "", scanInfoErrorText(m)
	}
	if _, ok := m["error"].(map[string]any); ok {
		if failed := scanInfoErrorText(m); failed != "" {
			return false, false, "", failed
		}
	}
	if st, _ := m["status"].(float64); st == 1 {
		scanned = true
	}
	zc0 = scanInfoCookieString(m, "cookie")
	if zc0 == "" {
		zc0 = scanInfoCookieString(m, "cookies")
	}
	if s, _ := m["z_c0"].(string); s != "" {
		zc0 = s
	}
	if at, _ := m["access_token"].(string); at != "" {
		return true, scanned, zc0, ""
	}
	if uid, ok := m["user_id"]; ok && uid != nil {
		switch v := uid.(type) {
		case string:
			if v != "" {
				return true, scanned, zc0, ""
			}
		case float64:
			if v != 0 {
				return true, scanned, zc0, ""
			}
		}
	}
	if zc0 != "" {
		return true, scanned, zc0, ""
	}
	if ls, _ := m["login_status"].(string); ls != "" {
		switch strings.ToUpper(strings.TrimSpace(ls)) {
		case "CONFIRMED", "LOGIN_SUCCESS", "SUCCESS", "OK", "LOGGED_IN":
			return true, scanned, zc0, ""
		}
	}
	if b, _ := m["success"].(bool); b {
		return true, scanned, zc0, ""
	}
	if b, _ := m["logged_in"].(bool); b {
		return true, scanned, zc0, ""
	}
	return false, scanned, zc0, ""
}

// scanInfoCookieString extracts the z_c0 value from a "a=b; c=d" cookie string
// carried in a scan_info body field.
func scanInfoCookieString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	for _, part := range strings.Split(s, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && name == "z_c0" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// scanInfoErrorText picks a human-readable error message from a scan_info body,
// preferring the nested error.message / error.name and then plain message.
func scanInfoErrorText(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		for _, k := range []string{"message", "name", "code"} {
			if s, ok := e[k].(string); ok && s != "" {
				return s
			}
			if n, ok := e[k].(float64); ok && n != 0 {
				return fmt.Sprintf("error %v", n)
			}
		}
	}
	if s, ok := m["message"].(string); ok && s != "" {
		return s
	}
	return ""
}

// ---- phone + verify code login (Android account protocol) ----
//
// Since the 2026 web dropped send_verify_code (HTTP 404), the phone route
// replays the first-party Android client on api.zhihu.com: encrypted device
// guest init -> captcha (when risk control asks) -> auth/digits (SMS) ->
// sign_in. The protocol, request bodies and the error-code branching live in
// phonelogin.go; this section only wires the resulting session into the web
// credential store. The functions below are thin, mutex-serialized wrappers
// the login UI (loginsrv.go) calls directly.

// phoneSendDigits requests the SMS code; when risk control demands a picture
// captcha the outcome carries the image to solve first.
func phoneSendDigits(phone string) (*zhihuPhoneDigitsOutcome, error) {
	return zhihuPhoneSendDigits(phone)
}

// phoneVerifyCaptcha validates the picture captcha and then sends the code.
func phoneVerifyCaptcha(phone, input string) (*zhihuPhoneDigitsOutcome, error) {
	return zhihuPhoneVerifyCaptcha(phone, input)
}

// applyPhoneWebSession folds a mobile sign-in token's cookie map into the
// shared web session (d_c0 + z_c0) and refreshes the post-login _xsrf/_zap so
// the data APIs authenticate with the freshly minted credentials.
func applyPhoneWebSession(token *zhihuPhoneToken) error {
	if token == nil {
		return errors.New("zhihu: phone sign-in returned no token")
	}
	start := time.Now()
	sess.mu.Lock()
	if dc0 := token.Cookies["d_c0"]; dc0 != "" {
		sess.dC0 = dc0
	}
	zc0 := token.Cookies["z_c0"]
	sess.zC0 = zc0
	sess.mu.Unlock()
	logf("zhihu: phone sign-in token: has_d_c0=%v has_z_c0=%v elapsed=%s",
		token.Cookies["d_c0"] != "", zc0 != "", time.Since(start).Round(time.Millisecond))
	if zc0 == "" {
		markSessionInvalid(errors.New("zhihu: 手机号登录未获得 z_c0"))
		return ErrNotLoggedIn
	}
	// Mint the session-bound _xsrf/_zap for the now-authenticated browser.
	warmupGet(zhihuSigninURL)
	sess.mu.RLock()
	webPrereq := sess.xsrf != "" || sess.zap != ""
	sess.mu.RUnlock()
	logf("zhihu: phone session web refresh: has_xsrf_or_zap=%v elapsed=%s", webPrereq, time.Since(start).Round(time.Millisecond))
	return nil
}

// ---- session persistence ----

// authFilePathFn is injectable so tests can route zhihu-auth.json writes away
// from the real user configuration without touching the process HOME.
var authFilePathFn = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "fedlet", "zhihu-auth.json")
}

func authFilePath() string { return authFilePathFn() }

func loadAuth() {
	data, err := os.ReadFile(authFilePath())
	if err != nil {
		return
	}
	var f authFileJSON
	if err := json.Unmarshal(data, &f); err != nil {
		log.Printf("zhihu: load auth parse error: %v", err)
		return
	}
	applyAuthFile(f)
}

// applyAuthFile folds a persisted auth file into the shared session. A file
// that was marked invalid (a rejected session) is never replayed: its z_c0 and
// _xsrf stay out of the runtime session and the login gateway takes over.
func applyAuthFile(f authFileJSON) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if f.DC0 != "" {
		sess.dC0 = f.DC0
	}
	sess.user = f.User
	if f.Status == AuthStatusInvalid {
		sess.zC0 = ""
		sess.xsrf = ""
		sess.status = AuthStatusInvalid
		return
	}
	sess.zC0 = f.ZC0
	sess.xsrf = f.XSRF
	if f.ZC0 != "" {
		sess.status = AuthStatusReady
	} else {
		sess.status = f.Status
	}
}

func saveAuth() {
	sess.mu.RLock()
	f := authFileJSON{
		DC0:    sess.dC0,
		ZC0:    sess.zC0,
		XSRF:   sess.xsrf,
		User:   sess.user,
		Status: sess.status,
	}
	sess.mu.RUnlock()
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		log.Printf("zhihu: save auth marshal error: %v", err)
		return
	}
	p := authFilePath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		log.Printf("zhihu: save auth mkdir error: %v", err)
		return
	}
	if err := os.WriteFile(p, data, 0600); err != nil {
		log.Printf("zhihu: save auth write error: %v", err)
	}
}
