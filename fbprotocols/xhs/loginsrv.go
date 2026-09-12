package xhs

// Self-contained login UI + risk-control panel.
//
// When a session is needed the protocol starts a throwaway HTTP server on a
// random 127.0.0.1 port, opens the system browser on it, and shuts itself down
// once the login finishes (or times out). The page offers:
//
//   - QR scan (auto-started via /api/sns/web/v1/login/qrcode/create, polled via
//     POST /api/qrcode/userinfo until confirmed, then the session is fetched
//     through GET /api/sns/web/v1/login/qrcode/status)
//   - phone + verify code (send code, then submit the code)
//   - Cookie paste (user copies a1/web_session from browser DevTools)
//   - risk panel: when a 461/471/472 request is blocked it renders the
//     app-scan verification QR (secondary verification) or the slider
//     pictures and a "已完成验证,继续" button that unblocks the round
//
// All flows verified against the jackwener and PeanutSplash clients (2026-09);
// exact payload fields that may drift are marked 待实测.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
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
	qrExpireAfter  = 120 * time.Second
	successHoldMs  = 1500
)

// login stages surfaced to the page.
const (
	stageIdle    = "idle"
	stageWaiting = "waiting" // QR shown, awaiting scan
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
		m["qr_code_url"] = "https://api.qrserver.com/v1/create-qr-code/?size=220x220&data=" + s.qrURL
		m["qr_url"] = s.qrURL
	}
	if s.errMsg != "" {
		m["error"] = s.errMsg
	}

	// Risk panel state merges into the same snapshot so the page can poll one
	// endpoint.
	if ch, url, imgs, active := pendingRisk(); active {
		m["risk"] = riskSnapshot(ch, url, imgs)
		m["risk_active"] = true
	}
	return m
}

func riskSnapshot(ch *riskChallenge, url string, imgs *riskCaptchaData) map[string]any {
	m := map[string]any{
		"status": ch.status,
		"type":   ch.verifyType,
	}
	if url != "" {
		m["verify_qr_code_url"] = "https://api.qrserver.com/v1/create-qr-code/?size=220x220&data=" + url
		m["verify_qr_url"] = url
	}
	if imgs != nil {
		if imgs.BackgroundURL != "" {
			m["slider_bg"] = imgs.BackgroundURL
		}
		if imgs.CaptchaURL != "" {
			m["slider_cap"] = imgs.CaptchaURL
		}
		if imgs.Hint != "" {
			m["hint"] = imgs.Hint
		}
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
	mu     sync.Mutex
	active bool
	opened bool
	lnAddr string
	stop   chan struct{}
	state  *loginState
}

// startLoginUI launches the self-contained login server on a random port and
// returns its local URL; the caller hands it to the browser. It is safe to call
// repeatedly; a second call while one UI is already running returns the
// existing URL.
func startLoginUI() (string, error) {
	ui.mu.Lock()
	if ui.active && uiAddrUsable(ui.lnAddr) {
		url := "http://" + ui.lnAddr + "/"
		ui.mu.Unlock()
		return url, nil
	}
	// Previous instance (if any) is dead/unusable: clear markers, build fresh.
	ui.active = false
	ui.opened = false
	ui.lnAddr = ""

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		ui.mu.Unlock()
		return "", fmt.Errorf("xhs: login listen: %w", err)
	}
	addr := ln.Addr().String()
	if !uiAddrUsable(addr) {
		_ = ln.Close()
		ui.mu.Unlock()
		return "", fmt.Errorf("xhs: login listener unusable addr %q", addr)
	}
	ui.lnAddr = addr
	ui.active = true
	ui.opened = false
	ui.state = &loginState{stage: stageIdle}
	ui.stop = make(chan struct{})
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
	mux.HandleFunc("/api/phone", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Phone string `json:"phone"`
		}
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Phone == "" {
			http.Error(w, "phone required", http.StatusBadRequest)
			return
		}
		if err := phoneSendCode(req.Phone, ""); err != nil {
			ls.set(stageFailed, err.Error())
			writeJSONMap(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSONMap(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/verify", func(w http.ResponseWriter, r *http.Request) {
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
		if err := phoneLogin(req.Phone, req.Code, ""); err != nil {
			ls.set(stageFailed, err.Error())
			writeJSONMap(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if err := verifySession(); err != nil {
			ls.set(stageFailed, err.Error())
			writeJSONMap(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		saveAuth()
		finalizeLogin(ls, "phone")
		writeJSONMap(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/cookie", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			A1      string            `json:"a1"`
			Session string            `json:"web_session"`
			Sec     string            `json:"web_session_sec"`
			WebID   string            `json:"webId"`
			Extra   map[string]string `json:"extra"`
		}
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		req.A1 = strings.TrimSpace(req.A1)
		req.Session = strings.TrimSpace(req.Session)
		if req.A1 == "" || req.Session == "" {
			writeJSONMap(w, map[string]any{"ok": false, "error": "a1 和 web_session 为必填项"})
			return
		}
		c := client()
		c.cookies.set("a1", req.A1)
		c.cookies.set(xhsWebSessionName, req.Session)
		if req.Sec != "" {
			c.cookies.set(webSessionSecCookie, req.Sec)
		}
		if req.WebID != "" {
			c.cookies.set("webId", req.WebID)
		}
		for k, v := range req.Extra {
			if k != "" && v != "" {
				c.cookies.set(k, v)
			}
		}
		c.syncFromJar()
		if err := verifySession(); err != nil {
			ls.set(stageFailed, err.Error())
			writeJSONMap(w, map[string]any{"ok": false, "error": "Cookie 无效或已过期: " + err.Error()})
			return
		}
		saveAuth()
		finalizeLogin(ls, "cookie")
		writeJSONMap(w, map[string]any{"ok": true, "user": AuthUser()})
	})
	// Risk panel: user confirms the captcha/secondary verification is done.
	mux.HandleFunc("/api/risk/continue", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		clearRisk()
		writeJSONMap(w, map[string]any{"ok": true})
	})

	srv := &http.Server{Handler: mux}
	go func() {
		_ = srv.Serve(ln)
	}()
	// Hand the URL to the browser only once the listener accepts: dial until
	// ready or give up (2s) instead of racing the Serve goroutine above.
	if !waitServing(addr, 2*time.Second) {
		_ = srv.Close()
		ui.mu.Lock()
		ui.active = false
		ui.opened = false
		ui.mu.Unlock()
		return "", errors.New("xhs: login server did not start serving")
	}
	go func() {
		select {
		case <-ui.stop:
		case <-time.After(loginTimeout): // hard cap: exit even if abandoned
		}
		// Invalidate immediately: any concurrent relogin must build a fresh
		// instance, never reuse a URL that is about to die.
		ui.mu.Lock()
		ui.active = false
		ui.opened = false
		ui.lnAddr = ""
		ui.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		ui.mu.Lock()
		ui.state = nil
		ui.mu.Unlock()
	}()

	// Kick off the QR flow immediately; the page just displays it.
	go runQRFlow(ls)

	return url, nil
}

// uiAddrUsable accepts only a real, bound loopback endpoint: a non-empty
// 127.0.0.1 host with a numeric port above 0, so "http://host:0/" style
// invalid links can never be produced or reused as a live UI URL.
func uiAddrUsable(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" || port == "" {
		return false
	}
	p, err := strconv.Atoi(port)
	return err == nil && p > 0
}

// waitServing polls a TCP endpoint until it accepts a connection or the
// timeout elapses. It guarantees the login page is actually listening before
// the browser is pointed at it.
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

// runQRFlow drives the QR login state machine in the background: create, poll
// the /api/qrcode/userinfo codeStatus until confirmed, then complete via
// /api/sns/web/v1/login/qrcode/status and fold the session into the jar.
func runQRFlow(ls *loginState) {
	q, err := qrCreate()
	if err != nil {
		ls.set(stageFailed, err.Error())
		return
	}
	ls.mu.Lock()
	ls.qrURL = q.URL
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
		status, userID, err := qrPollStatus(q)
		if err != nil {
			select {
			case <-ui.stop:
				return
			case <-time.After(qrPollInterval):
			}
			continue
		}
		switch status {
		case 2:
			ls.set(stageScanned, "")
			if err := qrComplete(q, userID); err != nil {
				ls.set(stageFailed, err.Error())
				return
			}
			if err := verifySession(); err != nil {
				ls.set(stageFailed, err.Error())
				return
			}
			saveAuth()
			finalizeLogin(ls, "qr")
			return
		case 3:
			ls.set(stageExpired, "")
			return
		case 1:
			ls.set(stageScanned, "")
		default: // 0 waiting
		}
		select {
		case <-ui.stop:
			return
		case <-time.After(qrPollInterval):
		}
	}
}

// finalizeLogin marks the successful login and shuts the UI down shortly after
// so the user can see the success page.
func finalizeLogin(ls *loginState, via string) {
	user := AuthUser()
	ls.mu.Lock()
	ls.stage = stageDone
	ls.user = user
	ls.mu.Unlock()
	c := client()
	a := c.opts.auth
	a.mu.Lock()
	a.loginMethod = via
	a.mu.Unlock()
	saveAuth()
	logPrefix("%s login ok user=%s", via, user)
	time.AfterFunc(successHoldMs, func() {
		ui.mu.Lock()
		defer ui.mu.Unlock()
		if ui.stop != nil {
			close(ui.stop)
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
// opener failure it always surfaces the manual URL.
func openLoginBrowser(url string) {
	if !isDesktopFn() {
		logPrefix("无桌面环境,请手动打开登录页: %s", url)
		return
	}
	if err := openUrlFunc(url); err != nil {
		logPrefix("自动打开浏览器失败(%v),请手动打开登录页: %s", err, url)
		return
	}
	logPrefix("browser opened at %s", url)
}

// openLoginBrowserOnce hands the URL to the browser at most once per UI
// lifetime; a failed open still surfaces the manual URL.
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

// htmlEscape is kept out-of-band; the page never echoes user input verbatim.

const loginPageHTML = `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<title>小红书登录 - fedlet</title>
<style>
body{font-family:system-ui,sans-serif;max-width:620px;margin:3rem auto;padding:0 1rem;color:#222}
h1{font-size:1.3rem}
.card{border:1px solid #ddd;border-radius:10px;padding:1.2rem;margin:1rem 0}
.qrimg{width:220px;height:220px;border:1px solid #eee;border-radius:8px}
input{padding:.5rem;width:100%;box-sizing:border-box;margin:.3rem 0;border:1px solid #ccc;border-radius:6px}
button{padding:.55rem 1.4rem;border:0;border-radius:6px;background:#ff2442;color:#fff;cursor:pointer;margin-top:.5rem}
button.blue{background:#0d69d5}
#state{font-weight:600}
.err{color:#b00020}
pre{white-space:normal;word-break:break-all;background:#f6f6f6;padding:.6rem;border-radius:6px}
.slider{max-width:100%}
.hint{color:#666;font-size:.85rem}
</style>
</head>
<body>
<h1>小红书登录 / 风控验证(fedlet xhs)</h1>
<div class="card">
<p id="state">初始化…</p>
<div id="qrbox" style="display:none">
  <img class="qrimg" id="qr" alt="QR">
  <p>用 <b>小红书 App</b> 的「扫一扫」登录;<br>或手机浏览器打开:<pre id="qrl"></pre></p>
</div>
<div id="userbox" style="display:none">登录成功:<b id="user"></b></div>
<div id="failed" class="err" style="display:none"></div>
</div>
<div class="card" id="riskcard" style="display:none">
<h2>风控验证</h2>
<p class="hint" id="riskhint"></p>
<p id="riskmsg"></p>
<div id="riskqr" style="display:none">
  <p>检测到安全验证。请用 <b>小红书 App</b>「扫一扫」验证(二次验证二维码):</p>
  <img class="qrimg" id="riskqr_img" alt="verify QR">
  <pre id="riskqr_url"></pre>
</div>
<div id="riskslider" style="display:none">
  <p>滑块验证:请<a id="riskslider_bg_link" href="#" target="_blank" rel="noopener">打开原图</a>查看方向,在原页面完成后点下方按钮继续。</p>
</div>
<button class="blue" id="riskbtn" onclick="riskContinue()">已完成验证,继续发送请求</button>
</div>
<div class="card">
<h2>手机号 + 验证码</h2>
<div id="phoneform">
  <input id="phone" type="tel" placeholder="手机号" autocomplete="tel">
  <button onclick="sendCode()">发送验证码</button>
</div>
<div id="codeform" style="display:none">
  <input id="code" type="text" placeholder="验证码" autocomplete="one-time-code">
  <button onclick="doVerify()">登录</button>
</div>
<p id="phonemsg" class="err" style="display:none;white-space:pre-wrap"></p>
</div>
<div class="card">
<h2>Cookie 登录(从浏览器复制)</h2>
<details>
<summary style="cursor:pointer;color:#0d69d5">点击展开:获取方法</summary>
<ol style="font-size:.9rem;color:#555;line-height:1.6">
<li>电脑浏览器打开 <b>xiaohongshu.com</b> 并登录</li>
<li>F12 打开开发者工具 → <b>Application</b> → <b>Cookies</b> → <code>xiaohongshu.com</code></li>
<li>找到并复制以下字段的值:</li>
<ul>
<li><b>a1</b>(必填) — 签名所需的浏览器指纹 cookie</li>
<li><b>web_session</b>(必填) — 登录会话令牌</li>
<li><b>web_session_sec</b>(可选) — 安全会话 cookie</li>
<li><b>webId</b>(可选) — 浏览器标识</li>
</ul>
<li>注意:Cookie 有效期有限,过期后需重新获取</li>
</ol>
</details>
<div id="cookieform">
  <input id="cookie_a1" type="text" placeholder="a1(必填)" autocomplete="off">
  <input id="cookie_session" type="text" placeholder="web_session(必填)" autocomplete="off">
  <input id="cookie_sec" type="text" placeholder="web_session_sec(可选)" autocomplete="off">
  <input id="cookie_webid" type="text" placeholder="webId(可选)" autocomplete="off">
  <button class="blue" onclick="cookieLogin()">使用 Cookie 登录</button>
</div>
<p id="cookiemsg" class="err" style="display:none;white-space:pre-wrap"></p>
</div>
<script>
const $=id=>document.getElementById(id);
const stage_txt={'idle':'准备二维码…','waiting':'等待扫码…','scanned':'已扫码,确认中…','done':'登录成功','expired':'二维码已过期,请刷新页面','failed':'登录失败'};
async function poll(){
  try{
    const r=await fetch('/api/state');const s=await r.json();
    $('state').textContent=stage_txt[s.stage]||s.stage;
    if(s.qr_code_url){$('qrbox').style.display='';$('qr').src=s.qr_code_url;$('qrl').textContent=s.qr_url;}
    if(s.stage==='done'){ $('qrbox').style.display='none';$('userbox').style.display='';$('user').textContent=s.user||'';}
    if(s.stage==='failed'){ $('failed').style.display='';$('failed').textContent=s.error||'未知错误';}
    renderRisk(s.risk);
  }catch(e){}
}
function renderRisk(rk){
  if(!rk){ $('riskcard').style.display='none'; return; }
  $('riskcard').style.display='';
  $('riskhint').textContent='请求被风控拦截(HTTP '+rk.status+' · Captcha '+rk.type+')';
  $('riskmsg').textContent='';
  let hasQR=false,hasSlide=false;
  if(rk.verify_qr_code_url){hasQR=true;$('riskqr').style.display='';$('riskqr_img').src=rk.verify_qr_code_url;$('riskqr_url').textContent=rk.verify_qr_url;}
  if(rk.slider_bg){hasSlide=true;$('riskslider').style.display='';$('riskslider_bg_link').href=rk.slider_bg;}
  if(!hasQR&&!hasSlide){$('riskmsg').textContent='未生成验证二维码(需手动在浏览器完成验证后继续)。';}
}
async function riskContinue(){
  await fetch('/api/risk/continue',{method:'POST'});
  $('riskcard').style.display='none';
}
async function sendCode(){
  const phone=$('phone').value.trim();
  const r=await fetch('/api/phone',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({phone})});
  const j=await r.json();
  if(j.ok){ $('phoneform').style.display='none';$('codeform').style.display='';$('phonemsg').style.display='none';
  } else { $('phonemsg').style.display='';$('phonemsg').textContent='发送验证码失败:\n'+j.error; }
}
async function doVerify(){
  const phone=$('phone').value.trim(),code=$('code').value.trim();
  const r=await fetch('/api/verify',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({phone,code})});
  const j=await r.json();
  if(j.ok){ $('phoneform').style.display='none';$('codeform').style.display='none';$('phonemsg').style.display='none';
  } else { $('phonemsg').style.display='';$('phonemsg').textContent='登录失败:\n'+j.error; }
}
poll();setInterval(poll,1500);
async function cookieLogin(){
  const a1=$('cookie_a1').value.trim(),web_session=$('cookie_session').value.trim();
  const web_session_sec=$('cookie_sec').value.trim(),webId=$('cookie_webid').value.trim();
  const msg=$('cookiemsg'); msg.style.display='none';
  if(!a1||!web_session){ msg.style.display=''; msg.textContent='a1 和 web_session 为必填项'; return; }
  try{
    const body={a1,web_session};
    if(web_session_sec) body.web_session_sec=web_session_sec;
    if(webId) body.webId=webId;
    const r=await fetch('/api/cookie',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
    const j=await r.json();
    if(j.ok){
      $('cookieform').style.display='none'; msg.style.display='none';
      $('qrbox').style.display='none'; $('phoneform').style.display='none'; $('codeform').style.display='none';
      $('state').textContent='登录成功'; $('userbox').style.display=''; $('user').textContent=j.user||'';
    } else { msg.style.display=''; msg.textContent='Cookie 登录失败:\n'+j.error; }
  }catch(e){ msg.style.display=''; msg.textContent='Cookie 登录失败:\n'+(e&&e.message||'网络错误'); }
}
</script>
</body>
</html>
`
