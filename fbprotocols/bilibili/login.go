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

	qrGenURL      = passportHost + "/x/passport-login/web/qrcode/generate"
	qrPollURL     = passportHost + "/x/passport-login/web/qrcode/poll"
	keyURL        = passportHost + "/x/passport-login/web/key"
	loginURL      = passportHost + "/x/passport-login/web/login"
	smsSendURL    = passportHost + "/x/passport-login/web/sms/send"
	smsLoginURL   = passportHost + "/x/passport-login/web/login/sms"
	smsCaptchaURL = passportHost + "/x/passport-login/captcha?source=main_web"
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

// SMS 登录人机验证记忆态:token/gt/challenge 由 /api/smscaptcha(或发送失败回退)自动获取,
// captcha_key 由发送成功时回填,供短信验证码登录兑换会话。
var (
	smsStateMu    sync.Mutex
	smsToken      string
	smsGT         string
	smsChallenge  string
	smsCaptchaKey string
)

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

// finalized reports whether a login method already completed. The background
// QR poller must stop touching the UI state once this flips, otherwise it
// clobbers the success view during the brief window before the UI shuts down.
func (s *loginState) finalized() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stage == stageDone
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
		token, gt, challenge := smsLastCaptcha()
		if req.Challenge == "" && challenge != "" {
			req.Challenge = challenge
		}
		needCaptcha := req.Challenge == "" || req.Validate == ""
		if needCaptcha && token == "" {
			if tk, g, ch, err := smsCaptchaParams(); err == nil {
				token, gt, challenge = tk, g, ch
				req.Challenge = ch
				needCaptcha = req.Challenge == "" || req.Validate == ""
			} else {
				ls.set(stageFailed, err.Error())
			}
		}
		form := smsSendForm(req.Phone, req.Validate, req.Challenge, req.Seccode)
		preview := smsFormPreview(form)
		if needCaptcha {
			writeJSONMap(w, map[string]any{
				"ok":           false,
				"need_captcha": true,
				"error":        "需要极验人机验证:浏览器打开 bilibili.com 完成滑块,粘贴 validate 后重发",
				"token":        token,
				"gt":           gt,
				"challenge":    req.Challenge,
				"params":       preview,
			})
			return
		}
		if err := smsSend(req.Phone, req.Validate, req.Challenge, req.Seccode); err != nil {
			ls.set(stageFailed, err.Error())
			writeJSONMap(w, map[string]any{"ok": false, "error": err.Error(), "params": preview})
			return
		}
		writeJSONMap(w, map[string]any{"ok": true, "params": preview})
	})
	mux.HandleFunc("/api/smscaptcha", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		token, gt, challenge, err := smsCaptchaParams()
		if err != nil {
			writeJSONMap(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSONMap(w, map[string]any{"ok": true, "token": token, "gt": gt, "challenge": challenge})
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
	var gen qrGenData
	// bilibili throttles web/qrcode/generate by silently returning code:0 with
	// an empty data payload. Retry with backoff; on persistent failure surface
	// the raw body so the real response shape is visible in the field.
	var genErr error
	for attempt := 0; attempt < 3; attempt++ {
		env, raw, err := qrGenFetch(qrGenURL)
		if err != nil {
			genErr = err
			break
		}
		gen, err = parseQRGen(env.Data)
		if err != nil {
			genErr = fmt.Errorf("bilibili: parse data %s: %w", qrGenURL, err)
			break
		}
		if gen.URL != "" && gen.QRCodeKey != "" {
			break
		}
		genErr = fmt.Errorf("二维码生成失败:缺少 qrcode_key (code=%d, data=%s)", env.Code, truncate(string(raw), 240))
		if attempt < 2 {
			log.Printf("bilibili: qrcode generate returned empty data (attempt %d): %s",
				attempt+1, truncate(string(raw), 240))
			time.Sleep(qrGenRetry[attempt])
		}
	}
	if genErr != nil {
		log.Printf("bilibili: qrcode generate failed: %v", genErr)
		ls.set(stageFailed, genErr.Error())
		return
	}
	ls.mu.Lock()
	ls.qrURL = gen.URL
	ls.stage = stageWaiting
	ls.mu.Unlock()

	deadline := time.Now().Add(qrExpireAfter)
	for {
		select {
		case <-ui.stop:
			return
		default:
		}
		if ls.finalized() {
			return
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
				Code         int    `json:"code"`
			} `json:"data"`
		}
		u := qrPollURL + "?qrcode_key=" + url.QueryEscape(gen.QRCodeKey)
		if err := getJSON(&poll, u, false); err != nil {
			if ls.finalized() {
				return
			}
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
		// The live API always returns envelope code:0 and reports the scan
		// state in data.code (86101 not-scanned, 86090 scanned, 86091
		// confirmed, 86038 expired). Fall back to the envelope for legacy
		// behavior where 0 meant scanned-and-confirmed.
		sc := qrScanState(poll.Data.Code, poll.Code)
		if ls.finalized() {
			return
		}
		switch sc {
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
		case 86038:
			ls.set(stageExpired, "二维码已失效,请刷新页面")
			return
		case 86090:
			ls.set(stageScanned, "")
		case 86091:
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
		default:
			// 86101 以及未知状态: 未扫码, 继续轮询.
		}
		select {
		case <-ui.stop:
			return
		case <-time.After(qrPollInterval):
		}
	}
}

// qrGenRetry stages the backoff between qrcode/generate attempts on transient
// empty-data throttling.
var qrGenRetry = []time.Duration{500 * time.Millisecond, time.Second}

// qrGenFetch performs the qrcode/generate call and returns the raw envelope
// plus the raw body so callers can surface the exact response on failure.
func qrGenFetch(u string) (*envResp, []byte, error) {
	body, status, err := doBili(http.MethodGet, u, nil)
	if err != nil {
		return nil, body, err
	}
	if status != http.StatusOK {
		return nil, body, fmt.Errorf("bilibili: http %d %s", status, truncate(string(body), 160))
	}
	var env envResp
	if err := json.Unmarshal(body, &env); err != nil {
		if looksHTML(body) {
			return nil, body, fmt.Errorf("%w %s", errUnverifiable, u)
		}
		return nil, body, fmt.Errorf("bilibili: parse %s: %w", u, err)
	}
	if env.Code != 0 {
		return &env, body, classifyAPIError(u, env.Code, env.Message)
	}
	return &env, body, nil
}

// qrGenData is the parsed data payload of qrcode/generate — the envelope's
// "data" object ({"url":...,"qrcode_key":...}), not the full response.
type qrGenData struct {
	URL       string `json:"url"`
	QRCodeKey string `json:"qrcode_key"`
}

// parseQRGen parses the qrcode/generate data payload, recovering qrcode_key
// from the scan URL query when the server omits the top-level field.
func parseQRGen(envData json.RawMessage) (qrGenData, error) {
	var g qrGenData
	if len(envData) > 0 {
		if err := json.Unmarshal(envData, &g); err != nil {
			return g, err
		}
	}
	if g.URL != "" && g.QRCodeKey == "" {
		if k := keyFromScanURL(g.URL); k != "" {
			g.QRCodeKey = k
		}
	}
	return g, nil
}

// keyFromScanURL extracts the qrcode_key query parameter from a scan URL,
// covering responses where the server omits the top-level qrcode_key field.
func keyFromScanURL(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.Query().Get("qrcode_key")
}

// qrScanState resolves the QR poll status, preferring data.code (the live API
// keeps the envelope at 0) and falling back to the envelope code (legacy API
// reported the state directly at the top level).
func qrScanState(dataCode, envCode int) int {
	if dataCode != 0 {
		return dataCode
	}
	return envCode
}

// finalizeLogin marks the successful login and shuts the UI down shortly after.
func finalizeLogin(ls *loginState, via string) {
	if jar.get("SESSDATA") != "" {
		_ = probeSession() // fill the account name; best-effort, keep the session
	}
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

// smsCaptchaParams 获取短信验证所需的人机验证参数:极验 token + gt + challenge。
func smsCaptchaParams() (token, gt, challenge string, err error) {
	var data struct {
		Type    string `json:"type"`
		Token   string `json:"token"`
		Geetest struct {
			Gt        string `json:"gt"`
			Challenge string `json:"challenge"`
		} `json:"geetest"`
	}
	if err = getJSON(&data, smsCaptchaURL, false); err != nil {
		return "", "", "", fmt.Errorf("bilibili: 获取短信验证参数失败: %w", err)
	}
	if data.Type != "geetest" || data.Token == "" || data.Geetest.Challenge == "" {
		return "", "", "", fmt.Errorf("bilibili: 短信验证参数异常 type=%q", data.Type)
	}
	smsRememberCaptcha(data.Token, data.Geetest.Gt, data.Geetest.Challenge)
	return data.Token, data.Geetest.Gt, data.Geetest.Challenge, nil
}

func smsRememberCaptcha(token, gt, challenge string) {
	smsStateMu.Lock()
	defer smsStateMu.Unlock()
	smsToken, smsGT, smsChallenge = token, gt, challenge
}

func smsLastCaptcha() (token, gt, challenge string) {
	smsStateMu.Lock()
	defer smsStateMu.Unlock()
	return smsToken, smsGT, smsChallenge
}

func smsSetCaptchaKey(key string) {
	smsStateMu.Lock()
	defer smsStateMu.Unlock()
	smsCaptchaKey = key
}

func smsLastCaptchaKey() string {
	smsStateMu.Lock()
	defer smsStateMu.Unlock()
	return smsCaptchaKey
}

// smsSendForm 构造将提交给 web/sms/send 的表单,不触网。极验字段只在齐备时携带,
// seccode 留空则按 validate+"|jordan" 自动补齐。
func smsSendForm(phone, validate, challenge, seccode string) url.Values {
	token, _, _ := smsLastCaptcha()
	form := url.Values{}
	form.Set("cid", "86")
	form.Set("tel", phone)
	form.Set("source", "main_web")
	if token != "" {
		form.Set("token", token)
	}
	if challenge != "" && validate != "" {
		form.Set("challenge", challenge)
		form.Set("validate", validate)
		if seccode == "" {
			seccode = validate + "|jordan"
		}
		form.Set("seccode", seccode)
	}
	return form
}

// smsFormPreview 把将提交给 web/sms/send 的表单渲染为只读文本,供页面披露实际发送值。
func smsFormPreview(form url.Values) string {
	var parts []string
	for _, k := range []string{"cid", "tel", "source", "token", "challenge", "validate", "seccode"} {
		parts = append(parts, k+"="+form.Get(k))
	}
	return strings.Join(parts, "\n")
}

// smsSend requests an SMS code. Like the password route, a risk-gated account
// demands a geetest triplet (token/challenge/validate/seccode); the form it
// actually posts is disclosed read-only on the page.
func smsSend(phone, validate, challenge, seccode string) error {
	form := smsSendForm(phone, validate, challenge, seccode)
	var data struct {
		CaptchaKey string `json:"captcha_key"`
	}
	if err := postForm(&data, smsSendURL, form); err != nil {
		return fmt.Errorf("bilibili: 发送短信验证码失败: %w", err)
	}
	if data.CaptchaKey != "" {
		smsSetCaptchaKey(data.CaptchaKey)
	}
	return nil
}

// smsLogin exchanges the SMS code for the session cookies on the current
// web/login/sms endpoint, redeeming the captcha_key returned by smsSend.
func smsLogin(phone, code string) error {
	ck := smsLastCaptchaKey()
	if ck == "" {
		return fmt.Errorf("bilibili: 请先发送短信验证码")
	}
	form := url.Values{}
	form.Set("cid", "86")
	form.Set("tel", phone)
	form.Set("code", code)
	form.Set("source", "main_web")
	form.Set("captcha_key", ck)
	form.Set("keep", "true")
	if err := postForm(nil, smsLoginURL, form); err != nil {
		return fmt.Errorf("bilibili: 短信验证码登录失败: %w", err)
	}
	if jar.get("SESSDATA") == "" {
		return fmt.Errorf("bilibili: 短信登录未获得 SESSDATA")
	}
	return nil
}
