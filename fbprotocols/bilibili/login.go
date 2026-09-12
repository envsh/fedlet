package bilibili

// Self-contained login UI.
//
// When a session is needed the protocol starts a throwaway HTTP server on a
// random 127.0.0.1 port, opens the system browser on it, and shuts itself down
// once the login finishes (or times out). The page offers:
//
//   - QR scan (auto-started: generate -> poll qrcode/poll until code 0, the
//     response Set-Cookies SESSDATA/bili_jct/DedeUserID)
//   - password + geetest (RSA-encrypted, the geetest validate/seccode fields
//     are optional — when risk control demands them the page asks the browser
//     user to complete the hosted challenge and paste the three fields back)
//   - SMS code (send + login; the geetest challenge may be demanded the same
//     way)
//   - Cookie paste (user copies SESSDATA/bili_jct from browser DevTools)
//
// QR is the primary route. The geetest-based routes need human help when the
// account is gated (challenge/validate/seccode), which the page reflects.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	loginTimeout   = 10 * time.Minute
	qrPollInterval = 3 * time.Second
	qrExpireAfter  = 180 * time.Second
	successHoldMs  = 1500

	qrGenURL    = passportHost + "/x/passport-login/web/qrcode/generate"
	qrPollURL   = passportHost + "/x/passport-login/web/qrcode/poll"
	keyURL      = passportHost + "/x/passport-login/web/key"
	loginURL    = passportHost + "/x/passport-login/web/login"
	smsSendURL  = passportHost + "/x/passport-login/web/sms/send"
	smsLoginURL = passportHost + "/x/passport-login/web/sms/login"
)

// login stages surfaced to the page.
const (
	stageIdle    = "idle"
	stageWaiting = "waiting"
	stageScanned = "scanned"
	stageDone    = "done"
	stageExpired = "expired"
	stageFailed  = "failed"
)

type loginState struct {
	mu     sync.Mutex
	stage  string
	qrURL  string
	errMsg string
	user   string
}

func (s *loginState) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]any{
		"stage": s.stage,
		"user":  s.user,
	}
	if s.qrURL != "" {
		m["qr_code_url"] = "https://api.qrserver.com/v1/create-qr-code/?size=220x220&data=" + url.QueryEscape(s.qrURL)
		m["qr_url"] = s.qrURL
	}
	if s.errMsg != "" {
		m["error"] = s.errMsg
	}
	return m
}

func (s *loginState) set(stage, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stage = stage
	if errMsg != "" {
		s.errMsg = errMsg
	}
}

var ui struct {
	mu        sync.Mutex
	active    bool
	opened    bool
	lnAddr    string
	stop      chan struct{}
	closeOnce sync.Once
	state     *loginState
}

// startLoginUIFn is a seam so the orchestrator's gateway can be unit-tested
// without actually binding a socket / opening a browser.
var startLoginUIFn = startLoginUI

// startLoginUI launches the self-contained login server on a random port and
// returns its URL; the caller hands it to the browser. Repeated calls while a
// UI is alive return the existing URL.
func startLoginUI() (string, error) {
	ui.mu.Lock()
	if ui.active && uiAddrUsable(ui.lnAddr) {
		url := "http://" + ui.lnAddr + "/"
		ui.mu.Unlock()
		return url, nil
	}
	ui.active = false
	ui.opened = false
	ui.lnAddr = ""

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		ui.mu.Unlock()
		return "", fmt.Errorf("bilibili: login listen: %w", err)
	}
	addr := ln.Addr().String()
	if !uiAddrUsable(addr) {
		_ = ln.Close()
		ui.mu.Unlock()
		return "", fmt.Errorf("bilibili: login listener unusable addr %q", addr)
	}
	ui.lnAddr = addr
	ui.active = true
	ui.opened = false
	ui.state = &loginState{stage: stageIdle}
	ui.stop = make(chan struct{})
	ui.closeOnce = sync.Once{}
	url := "http://" + addr + "/"
	ui.mu.Unlock()

	mux := http.NewServeMux()
	ls := ui.state
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		serveLoginPage(w, ls)
	})
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		writeJSONMap(w, ls.snapshot())
	})
	mux.HandleFunc("/api/qrstart", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		ls.mu.Lock()
		ls.qrURL = ""
		ls.errMsg = ""
		ls.stage = stageIdle
		ls.mu.Unlock()
		go runQRFlow(ls)
		writeJSONMap(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/password", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Username  string `json:"username"`
			Password  string `json:"password"`
			Validate  string `json:"validate"`
			Challenge string `json:"challenge"`
			Seccode   string `json:"seccode"`
		}
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" || req.Password == "" {
			http.Error(w, "username and password required", http.StatusBadRequest)
			return
		}
		if err := passwordLogin(req); err != nil {
			ls.set(stageFailed, err.Error())
			writeJSONMap(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		finalizeLogin(ls, "password")
		writeJSONMap(w, map[string]any{"ok": true, "user": AuthUser()})
	})
	mux.HandleFunc("/api/sms", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Phone     string `json:"phone"`
			Validate  string `json:"validate"`
			Challenge string `json:"challenge"`
			Seccode   string `json:"seccode"`
		}
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Phone == "" {
			http.Error(w, "phone required", http.StatusBadRequest)
			return
		}
		if err := smsSend(req); err != nil {
			ls.set(stageFailed, err.Error())
			writeJSONMap(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSONMap(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/smslogin", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Phone string `json:"phone"`
			Code  string `json:"code"`
		}
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Phone == "" || req.Code == "" {
			http.Error(w, "phone and code required", http.StatusBadRequest)
			return
		}
		if err := smsLogin(req.Phone, req.Code); err != nil {
			ls.set(stageFailed, err.Error())
			writeJSONMap(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		finalizeLogin(ls, "sms")
		writeJSONMap(w, map[string]any{"ok": true, "user": AuthUser()})
	})
	mux.HandleFunc("/api/cookie", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Sessdata      string `json:"SESSDATA"`
			BiliJCT       string `json:"bili_jct"`
			DedeUserID    string `json:"DedeUserID"`
			DedeUserCkMd5 string `json:"DedeUserID__ckMd5"`
			RefreshToken  string `json:"refresh_token"`
		}
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		req.Sessdata = strings.TrimSpace(req.Sessdata)
		req.BiliJCT = strings.TrimSpace(req.BiliJCT)
		if req.Sessdata == "" || req.BiliJCT == "" {
			writeJSONMap(w, map[string]any{"ok": false, "error": "SESSDATA 和 bili_jct 为必填项"})
			return
		}
		jar.set("SESSDATA", req.Sessdata)
		jar.set("bili_jct", req.BiliJCT)
		jar.set("DedeUserID", req.DedeUserID)
		jar.set("DedeUserID__ckMd5", req.DedeUserCkMd5)
		auth.mu.Lock()
		auth.refreshToken = req.RefreshToken
		auth.mu.Unlock()
		if err := probeSession(); err != nil {
			ls.set(stageFailed, err.Error())
			writeJSONMap(w, map[string]any{"ok": false, "error": "Cookie 无效或已过期: " + err.Error()})
			return
		}
		finalizeLogin(ls, "cookie")
		writeJSONMap(w, map[string]any{"ok": true, "user": AuthUser()})
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	if !waitServing(addr, 2*time.Second) {
		_ = srv.Close()
		ui.mu.Lock()
		ui.active = false
		ui.opened = false
		ui.mu.Unlock()
		return "", errors.New("bilibili: login server did not start serving")
	}
	go func() {
		select {
		case <-ui.stop:
		case <-time.After(loginTimeout):
		}
		ui.mu.Lock()
		ui.closeOnce.Do(func() { close(ui.stop) })
		ui.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		ui.mu.Lock()
		ui.active = false
		ui.opened = false
		ui.lnAddr = ""
		ui.state = nil
		ui.mu.Unlock()
	}()
	go runQRFlow(ls)

	return url, nil
}

func uiAddrUsable(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" || port == "" {
		return false
	}
	p, err := strconv.Atoi(port)
	return err == nil && p > 0
}

func waitServing(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

var qrFlowMu sync.Mutex

func runQRFlow(ls *loginState) {
	if !qrFlowMu.TryLock() {
		return
	}
	defer qrFlowMu.Unlock()
	if !isDesktop() {
		ls.set(stageFailed, "当前环境无桌面,扫码登录不可用;请使用 Cookie 登录")
		return
	}
	var gen struct {
		Code int `json:"code"`
		Data struct {
			URL       string `json:"url"`
			QRCodeKey string `json:"qrcode_key"`
		} `json:"data"`
	}
	if err := getJSON(&gen, qrGenURL, false); err != nil {
		ls.set(stageFailed, err.Error())
		return
	}
	if gen.Data.URL == "" || gen.Data.QRCodeKey == "" {
		ls.set(stageFailed, "二维码生成失败:缺少 qrcode_key")
		return
	}
	ls.mu.Lock()
	ls.qrURL = gen.Data.URL
	ls.stage = stageWaiting
	ls.mu.Unlock()

	deadline := time.Now().Add(qrExpireAfter)
	for {
		select {
		case <-ui.stop:
			return
		default:
		}
		if time.Now().After(deadline) {
			ls.set(stageExpired, "")
			return
		}
		var poll struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				URL          string `json:"url"`
				RefreshToken string `json:"refresh_token"`
			} `json:"data"`
		}
		u := qrPollURL + "?qrcode_key=" + url.QueryEscape(gen.Data.QRCodeKey)
		if err := getJSON(&poll, u, false); err != nil {
			if errors.Is(err, errUnverifiable) {
				ls.set(stageFailed, "二维码轮询被风控(HTML),请刷新重试或改用 Cookie 登录")
				return
			}
			select {
			case <-ui.stop:
				return
			case <-time.After(qrPollInterval):
			}
			continue
		}
		switch poll.Code {
		case 0:
			ls.set(stageScanned, "")
			auth.mu.Lock()
			auth.refreshToken = poll.Data.RefreshToken
			auth.mu.Unlock()
			if jar.get("SESSDATA") == "" {
				ls.set(stageFailed, "扫码登录未获得 SESSDATA cookie")
				return
			}
			if err := probeSession(); err != nil {
				ls.set(stageFailed, err.Error())
				return
			}
			finalizeLogin(ls, "qr")
			return
		case 86038, 86101:
			ls.set(stageExpired, "二维码已失效,请刷新页面")
			return
		case 86090, 86091:
			ls.set(stageScanned, "")
		default:
		}
		select {
		case <-ui.stop:
			return
		case <-time.After(qrPollInterval):
		}
	}
}

// finalizeLogin marks the successful login and shuts the UI down shortly after.
func finalizeLogin(ls *loginState, via string) {
	user := AuthUser()
	ls.mu.Lock()
	ls.stage = stageDone
	ls.user = user
	ls.mu.Unlock()
	setLoginMethod(via)
	saveAuth()
	authMu.Lock()
	reLogin.nextAt = time.Time{}
	authMu.Unlock()
	log.Printf("bilibili: %s login ok user=%s", via, user)
	time.AfterFunc(successHoldMs, func() {
		ui.mu.Lock()
		defer ui.mu.Unlock()
		if ui.stop != nil {
			ui.closeOnce.Do(func() { close(ui.stop) })
		}
	})
}

func writeJSONMap(w http.ResponseWriter, m map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m)
}

func isDesktop() bool {
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	case "linux":
		return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	}
	return false
}

var (
	openUrlFunc = openURL
	isDesktopFn = isDesktop
)

// openURL opens url via the first working candidate: $BROWSER, the platform
// opener (open / cmd start / xdg-open), then well-known browser names in PATH.
func openURL(url string) error {
	if b := strings.Fields(os.Getenv("BROWSER")); len(b) > 0 {
		if err := runOpenAtom(b[0], append(b[1:], url)); err == nil {
			return nil
		}
	}
	switch runtime.GOOS {
	case "darwin":
		return runOpenAtom("open", []string{url})
	case "windows":
		return runOpenAtom("cmd", []string{"/c", "start", "", url})
	default:
		if err := runOpenAtom("xdg-open", []string{url}); err == nil {
			return nil
		}
	}
	var errs []string
	for _, name := range []string{"firefox", "firefox-esr", "chromium", "chromium-browser", "google-chrome", "microsoft-edge"} {
		p, perr := exec.LookPath(name)
		if perr != nil {
			continue
		}
		if err := runOpenAtom(p, []string{url}); err != nil {
			errs = append(errs, name)
			continue
		}
		return nil
	}
	if len(errs) > 0 {
		return fmt.Errorf("browser candidates failed: %s", strings.Join(errs, ", "))
	}
	return errors.New("no browser opener available")
}

// runOpenAtom starts one opener and watches it up to 2s: a quick nonzero exit
// (the xdg-open no-handler class now surfaced instead of swallowed by Start);
// an atom still running after the window is treated as a successful handoff.
// The 8s context is a backstop so a wedged opener can never block us.
func runOpenAtom(name string, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-time.After(2 * time.Second):
		return nil
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// openLoginBrowser opens the login page when a desktop exists; headless or on
// opener failure it always surfaces the manual URL instead of failing silently.
func openLoginBrowser(url string) {
	if !isDesktopFn() {
		log.Printf("bilibili: 无桌面环境,请手动打开登录页: %s", url)
		return
	}
	if err := openUrlFunc(url); err != nil {
		log.Printf("bilibili: 自动打开浏览器失败(%v),请手动打开登录页: %s", err, url)
		return
	}
	log.Printf("bilibili: browser opened at %s", url)
}

// openLoginBrowserOnce hands the URL to the browser at most once per UI
// lifetime so repeated relogin triggers never stack tabs; a failed open still
// surfaces the manual URL.
func openLoginBrowserOnce(url string) {
	ui.mu.Lock()
	if ui.opened {
		ui.mu.Unlock()
		return
	}
	ui.opened = true
	ui.mu.Unlock()
	openLoginBrowser(url)
}

func serveLoginPage(w http.ResponseWriter, _ *loginState) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(loginPageHTML))
}

// ---- password + SMS backends ----

// passwordLogin RSA-encrypts the entered password with the passport public key
// (hash prefix + raw password) and posts the web login form. The geetest
// validate/challenge/seccode triplet is optional: risk-gated accounts require
// the human to complete the hosted slider and paste the three fields back into
// the page.
func passwordLogin(req struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	Validate  string `json:"validate"`
	Challenge string `json:"challenge"`
	Seccode   string `json:"seccode"`
}) error {
	var key struct {
		Data struct {
			Hash string `json:"hash"`
			Key  string `json:"key"`
		} `json:"data"`
	}
	if err := getJSON(&key, keyURL, false); err != nil {
		return fmt.Errorf("bilibili: 获取密码密钥失败: %w", err)
	}
	enc, err := rsaEncrypt(key.Data.Key, key.Data.Hash+req.Password)
	if err != nil {
		return fmt.Errorf("bilibili: 密码加密失败: %w", err)
	}
	form := url.Values{}
	form.Set("username", req.Username)
	form.Set("password", enc)
	form.Set("keep", "true")
	form.Set("source", "main_web")
	if req.Challenge != "" && req.Validate != "" {
		form.Set("challenge", req.Challenge)
		form.Set("validate", req.Validate)
		form.Set("seccode", req.Seccode)
	}
	if err := postForm(nil, loginURL, form); err != nil {
		return fmt.Errorf("bilibili: 密码登录失败: %w", err)
	}
	if jar.get("SESSDATA") == "" {
		return fmt.Errorf("bilibili: 密码登录未获得 SESSDATA(可能需要极验人机验证)")
	}
	return nil
}

// rsaEncrypt encrypts a string with a PEM PKCS#1/PKIX public key (PKCS1v15).
func rsaEncrypt(pubPEM, plain string) (string, error) {
	block, _ := pem.Decode([]byte(pubPEM))
	if block == nil {
		return "", errors.New("passport returned no public key")
	}
	var pub *rsa.PublicKey
	var err error
	if block.Type == "RSA PUBLIC KEY" {
		pub, err = x509.ParsePKCS1PublicKey(block.Bytes)
	} else {
		var anyKey any
		anyKey, err = x509.ParsePKIXPublicKey(block.Bytes)
		if err == nil {
			var ok bool
			pub, ok = anyKey.(*rsa.PublicKey)
			if !ok {
				err = errors.New("public key is not RSA")
			}
		}
	}
	if err != nil {
		return "", err
	}
	enc, err := rsa.EncryptPKCS1v15(rand.Reader, pub, []byte(plain))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

// smsSend requests an SMS code. Like the password route, a risk-gated account
// demands a geetest triplet (optional fields). Endpoint layout verified against
// bilibili-API-collect (待现场核实).
func smsSend(req struct {
	Phone     string `json:"phone"`
	Validate  string `json:"validate"`
	Challenge string `json:"challenge"`
	Seccode   string `json:"seccode"`
}) error {
	form := url.Values{}
	form.Set("cid", "86")
	form.Set("tel", req.Phone)
	form.Set("source", "main_web")
	if req.Challenge != "" && req.Validate != "" {
		form.Set("challenge", req.Challenge)
		form.Set("validate", req.Validate)
		form.Set("seccode", req.Seccode)
	}
	if err := postForm(nil, smsSendURL, form); err != nil {
		return fmt.Errorf("bilibili: 发送短信验证码失败: %w", err)
	}
	return nil
}

// smsLogin exchanges the SMS code for the session cookies.
func smsLogin(phone, code string) error {
	form := url.Values{}
	form.Set("cid", "86")
	form.Set("tel", phone)
	form.Set("code", code)
	form.Set("source", "main_web")
	form.Set("keep", "true")
	if err := postForm(nil, smsLoginURL, form); err != nil {
		return fmt.Errorf("bilibili: 短信验证码登录失败: %w", err)
	}
	if jar.get("SESSDATA") == "" {
		return fmt.Errorf("bilibili: 短信登录未获得 SESSDATA")
	}
	return nil
}
