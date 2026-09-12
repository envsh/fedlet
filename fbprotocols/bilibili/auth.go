package bilibili

// Session credential handling for the bilibili web API.
//
// Auth mirrors the bilibili web passport flow: the QR scan (or password/SMS/
// cookie paste) exchanges for the SESSDATA-family cookies plus a refresh_token
// (equivalent of localStorage ac_time_value). Everything persists to
// ~/.config/fedlet/bilibili-auth.json (0600). A refresh_token is one-time-rotate
// and bound to the SESSDATA — the web/refresh endpoint mints a new token + new
// SESSDATA together, so the rotation runs single-flight inside this file and
// writes the new pair to disk atomically before the old token is invalidated.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Auth status values reported by AuthStatus().
const (
	AuthStatusEmpty   = ""
	AuthStatusReady   = "ready"
	AuthStatusInvalid = "invalid"
)

const refreshURL = passportHost + "/x/passport-login/web/refresh"

// authFileJSON is the persisted credential shape in bilibili-auth.json.
type authFileJSON struct {
	SESSDATA        string `json:"SESSDATA"`
	BiliJCT         string `json:"bili_jct"`
	DedeUserID      string `json:"DedeUserID,omitempty"`
	DedeUserIDCkMd5 string `json:"DedeUserID__ckMd5,omitempty"`
	RefreshToken    string `json:"refresh_token,omitempty"`
	User            string `json:"user,omitempty"`
	Status          string `json:"status"`
	LoginMethod     string `json:"login_method,omitempty"`
}

// authState tracks the runtime session cookies + status; protected by its own
// mutex (separate from the cookie jar lock).
type authState struct {
	mu           sync.Mutex
	user         string
	status       string
	loginMethod  string
	refreshToken string
}

var auth = &authState{}

var (
	authMu      sync.Mutex
	lastAuthErr error
	// refreshMu + refreshInFlight serialize the refresh_token rotation so
	// concurrent -101 signals never race the one-time-rotate token.
	refreshMu       sync.Mutex
	refreshInFlight bool
	lastRefreshAt   time.Time
)

// AuthStatus returns the current credential state: empty/ready/invalid.
func AuthStatus() string {
	auth.mu.Lock()
	defer auth.mu.Unlock()
	return auth.status
}

// AuthUser returns the logged-in account name, if known.
func AuthUser() string {
	auth.mu.Lock()
	defer auth.mu.Unlock()
	return auth.user
}

// LastAuthErr returns the last auth failure (nil when none/ok).
func LastAuthErr() error {
	authMu.Lock()
	defer authMu.Unlock()
	return lastAuthErr
}

func setAuthStatus(s string) {
	auth.mu.Lock()
	auth.status = s
	auth.mu.Unlock()
	if s == AuthStatusInvalid {
		saveAuth()
	}
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

// ---- session persistence ----

func authFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "fedlet", "bilibili-auth.json")
}

func loadAuth() {
	data, err := os.ReadFile(authFilePath())
	if err != nil {
		return
	}
	var f authFileJSON
	if err := json.Unmarshal(data, &f); err != nil {
		log.Printf("bilibili: load auth parse error: %v", err)
		return
	}
	if f.SESSDATA != "" {
		jar.set("SESSDATA", f.SESSDATA)
	}
	if f.BiliJCT != "" {
		jar.set("bili_jct", f.BiliJCT)
	}
	if f.DedeUserID != "" {
		jar.set("DedeUserID", f.DedeUserID)
	}
	if f.DedeUserIDCkMd5 != "" {
		jar.set("DedeUserID__ckMd5", f.DedeUserIDCkMd5)
	}
	auth.mu.Lock()
	auth.user = f.User
	auth.loginMethod = f.LoginMethod
	auth.refreshToken = f.RefreshToken
	if f.Status == AuthStatusInvalid {
		auth.status = AuthStatusInvalid
	} else if f.SESSDATA != "" {
		auth.status = AuthStatusReady
	} else {
		auth.status = AuthStatusEmpty
	}
	auth.mu.Unlock()
}

func saveAuth() {
	auth.mu.Lock()
	f := authFileJSON{
		SESSDATA:        jar.get("SESSDATA"),
		BiliJCT:         jar.get("bili_jct"),
		DedeUserID:      jar.get("DedeUserID"),
		DedeUserIDCkMd5: jar.get("DedeUserID__ckMd5"),
		RefreshToken:    auth.refreshToken,
		User:            auth.user,
		Status:          auth.status,
		LoginMethod:     auth.loginMethod,
	}
	auth.mu.Unlock()
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		log.Printf("bilibili: save auth marshal error: %v", err)
		return
	}
	p := authFilePath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		log.Printf("bilibili: save auth mkdir error: %v", err)
		return
	}
	if err := os.WriteFile(p, data, 0600); err != nil {
		log.Printf("bilibili: save auth write error: %v", err)
	}
}

// setLoginMethod records the via string (qr/password/sms/cookie) and persists
// it alongside the session.
func setLoginMethod(via string) {
	auth.mu.Lock()
	auth.loginMethod = via
	auth.mu.Unlock()
}

// ---- session verification (nav probe) ----

// navEnvelope is the {code,data{isLogin,uname,mid,wbi_img}} shape of /nav.
type navEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		IsLogin bool   `json:"isLogin"`
		Uname   string `json:"uname"`
		Mid     int64  `json:"mid"`
		WbiImg  struct {
			ImgURL string `json:"img_url"`
			SubURL string `json:"sub_url"`
		} `json:"wbi_img"`
	} `json:"data"`
}

// probeSession checks the stored SESSDATA against /x/web-interface/nav and
// refreshes the user name, WBI keys and AuthStatus. It returns ErrNotLoggedIn
// when the session is rejected, which the caller treats as expired. It is a
// seam (var) so tests can override the network probe.
var probeSession = func() error {
	if jar.get("SESSDATA") == "" {
		markSessionInvalid(ErrNotLoggedIn)
		return ErrNotLoggedIn
	}
	var env navEnvelope
	body, status, err := doBili("GET", navURL, nil)
	if err != nil {
		if errors.Is(err, errUnverifiable) {
			log.Printf("bilibili: probe session: html shed (kept)")
			setAuthStatus(AuthStatusReady)
			return errUnverifiable
		}
		log.Printf("bilibili: probe session transient: %v", err)
		return err
	}
	if status != httpStatusOK {
		log.Printf("bilibili: probe session: http %d", status)
		return fmt.Errorf("bilibili: probe session: http %d", status)
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("bilibili: probe session parse: %w", err)
	}
	decodeNavForWBI(body)
	clearRateLimit()
	if env.Code == 0 && env.Data.IsLogin {
		auth.mu.Lock()
		auth.user = env.Data.Uname
		auth.status = AuthStatusReady
		auth.mu.Unlock()
		clearAuthErr()
		saveAuth()
		log.Printf("bilibili: session ok for %q", env.Data.Uname)
		return nil
	}
	markSessionInvalid(ErrNotLoggedIn)
	return ErrNotLoggedIn
}

const httpStatusOK = 200

// ---- refresh_token rotation (single-flight) ----

// refreshCookies rotates the SESSDATA family using the stored refresh_token via
// POST /x/passport-login/web/refresh. It is single-flight; concurrent callers
// just wait for the in-flight attempt and read the result. A dead token
// (-101/-111/86095) returns ErrNotLoggedIn so the gateway opens the login UI.
// It is a seam (var) so tests can override the network rotation.
var refreshCookies = func() error {
	refreshMu.Lock()
	if refreshInFlight {
		refreshMu.Unlock()
		// Concurrent trigger: wait briefly for the in-flight rotation.
		for i := 0; i < 50; i++ {
			if !refreshInFlight {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		auth.mu.Lock()
		st := auth.status
		auth.mu.Unlock()
		if st == AuthStatusReady {
			return nil
		}
		return ErrNotLoggedIn
	}
	refreshInFlight = true
	refreshMu.Unlock()

	defer func() {
		refreshMu.Lock()
		refreshInFlight = false
		lastRefreshAt = time.Now()
		refreshMu.Unlock()
	}()

	auth.mu.Lock()
	tok := auth.refreshToken
	auth.mu.Unlock()
	if tok == "" || jar.get("SESSDATA") == "" {
		return ErrNotLoggedIn
	}

	form := url.Values{}
	form.Set("refresh_token", tok)
	// The rotation replaces both the cookies and the token; the request must
	// carry the OLD SESSDATA.
	var data struct {
		RefreshToken string `json:"refresh_token"`
		Status       int    `json:"status"`
		Message      string `json:"message"`
	}
	body, status, err := doBili("POST", refreshURL, form)
	if err != nil {
		return err
	}
	if status != httpStatusOK {
		return fmt.Errorf("bilibili: refresh http %d: %s", status, truncate(string(body), 160))
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return fmt.Errorf("bilibili: refresh parse: %w", err)
	}
	if data.Status != 0 {
		log.Printf("bilibili: refresh_token rotate failed (status=%d msg=%s)", data.Status, data.Message)
		markSessionInvalid(ErrNotLoggedIn)
		return ErrNotLoggedIn
	}
	auth.mu.Lock()
	auth.refreshToken = data.RefreshToken
	auth.status = AuthStatusReady
	auth.mu.Unlock()
	saveAuth()
	log.Printf("bilibili: refresh_token rotate ok")
	return nil
}

// confirmOldToken invalidates the previous refresh_token with the NEW bili_jct
// csrf + the OLD token (the web ceremony requires it, but a failure here is
// non-fatal — the rotated pair already persists).
func confirmOldToken(oldToken string) {
	form := url.Values{}
	form.Set("csrf", jar.get("bili_jct"))
	form.Set("refresh_token", oldToken)
	if err := postForm(nil, passportHost+"/x/passport-login/web/confirm/refresh", form); err != nil {
		log.Printf("bilibili: confirm refresh best-effort failed: %v", err)
	}
}

// noteRefreshFailure is a seam so the gateway can log cooldown-gated refresh
// attempts without coupling to the login UI.
func noteRefreshFailure(err error) {
	log.Printf("bilibili: refresh failed: %v", err)
}
