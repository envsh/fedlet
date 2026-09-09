package zhihu

// Session credential handling for the Zhihu web API.
//
// Authentication mirrors zhihu-plus / pyzhihu-cli: either the phone+verify-code
// route or the QR scan route of the web login API, both exchange for a z_c0
// session cookie persisted to ~/.config/fedlet/zhihu-auth.json (0600). The
// visitor cookie d_c0 is generated fresh and feeds the signature; both the hot
// list and notification feeds run under the main z_c0 session.
//
// Endpoint details were derived from public zhihu-plus / pyzhihu-cli sources
// and are marked 待实测 (to-be-verified against the live site) where the exact
// field layout was not individually confirmed.

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

// Auth status values reported by AuthStatus().
const (
	AuthStatusEmpty   = ""
	AuthStatusReady   = "ready"
	AuthStatusInvalid = "invalid"
)

const (
	meURL          = "https://www.zhihu.com/api/v4/me"
	qrBaseURL      = "https://www.zhihu.com/api/v3/account/api/login/qrcode"
	sendCodeURL    = "https://www.zhihu.com/api/v3/account/api/send_verify_code"
	mobileLoginURL = "https://www.zhihu.com/api/v3/account/api/login"
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
	if !sess.hasZ() {
		markSessionInvalid(errors.New("zhihu: no session (z_c0 missing)"))
		return ErrNotLoggedIn
	}
	var me meResp
	if err := getJSON(&me, meURL, true); err != nil {
		if errors.Is(err, ErrNotLoggedIn) {
			markSessionInvalid(err)
			return err
		}
		return fmt.Errorf("zhihu: verify session: %w", err)
	}
	setAuthUser(me.Name)
	clearAuthErr()
	setAuthStatus(AuthStatusReady)
	if me.Name != "" {
		log.Printf("zhihu: session ok for %q", me.Name)
	}
	return nil
}

// ---- QR login (pyzhihu-cli / zhihu-plus-plus style, official flow) ----
//
// The web login now uses POST /qrcode to obtain the token and GET
// /qrcode/{token}/scan_info for status polling (GET on /qrcode answers 405;
// /scan + /confirm are the legacy/non-official endpoints). scan_info status
// contract: 0 = not scanned, 1 = scanned/awaiting confirm; success is signalled
// by access_token / user_id in the body, a z_c0 cookie value inside the body
// ("cookie"/"cookies"/"z_c0" fields) or a Set-Cookie z_c0.

type qrBeginResp struct {
	Code        int    `json:"code"`
	Token       string `json:"token"`
	QrcodeToken string `json:"qrcode_token"`
	URL         string `json:"url"`
	Link        string `json:"link"`
	Expires     int    `json:"expires"`
}

// qrBegin obtains a new scan token and the display URL. The API requires a
// POST; a GET is rejected with 405.
func qrBegin() (token, url string, err error) {
	var r qrBeginResp
	if err := postJSON(&r, qrBaseURL, []byte("{}"), false); err != nil {
		return "", "", fmt.Errorf("zhihu: qr begin: %w", err)
	}
	token = r.Token
	if token == "" {
		token = r.QrcodeToken
	}
	url = r.URL
	if url == "" {
		url = r.Link
	}
	if token == "" {
		return "", "", errors.New("zhihu: qr begin returned no token")
	}
	return token, url, nil
}

// qrScanInfo polls the official scan_info endpoint once.
func qrScanInfo(token string) ([]byte, int, error) {
	if token == "" {
		return nil, 0, errors.New("zhihu: qr scan_info: empty token")
	}
	return getRaw(http.MethodGet, qrBaseURL+"/"+token+"/scan_info", nil, false)
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

// ---- phone + verify code login ----

// phoneSendCode requests a verify code to be sent to a mobile number.
// Endpoint details are from public zhihu login flows; field handling is
// intentionally tolerant of variations (待实测).
func phoneSendCode(phone string) error {
	if phone == "" {
		return errors.New("zhihu: empty phone")
	}
	payload, _ := json.Marshal(map[string]string{"phone": phone})
	body, status, err := getRaw(http.MethodPost, sendCodeURL, payload, false)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("zhihu: send code http %d %s", status, truncate(string(body), 120))
	}
	return nil
}

// phoneLogin exchanges the phone + verify code for a z_c0 session.
func phoneLogin(phone, code string) error {
	if phone == "" || code == "" {
		return errors.New("zhihu: phone and code required")
	}
	payload, _ := json.Marshal(map[string]string{"phone": phone, "code": code, "remember": "true"})
	resp, err := postRawCapture(mobileLoginURL, payload)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("zhihu: phone login http %d %s", resp.StatusCode, truncate(string(resp.Body), 120))
	}
	if !sess.hasZ() {
		return errors.New("zhihu: phone login returned no z_c0")
	}
	return nil
}

// ---- session persistence ----

func authFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "fedlet", "zhihu-auth.json")
}

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
	sess.mu.Lock()
	if f.DC0 != "" {
		sess.dC0 = f.DC0
	}
	sess.zC0 = f.ZC0
	sess.xsrf = f.XSRF
	sess.user = f.User
	if f.ZC0 != "" && f.Status != AuthStatusInvalid {
		sess.status = AuthStatusReady
	} else {
		sess.status = f.Status
	}
	sess.mu.Unlock()
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
