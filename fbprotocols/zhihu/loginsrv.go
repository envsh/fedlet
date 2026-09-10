package zhihu

// Self-contained login UI.
//
// When a session is needed the protocol starts a throwaway HTTP server on a
// random 127.0.0.1 port (never localhost, to avoid ::1/dual-stack surprises),
// opens the system browser on it, and shuts itself down once the login
// finishes (or times out). The page offers the login routes:
//
//   - QR scan (auto-started: token, poll scan_info, capture z_c0)
//   - phone + verify code (Android account protocol on api.zhihu.com:
//     encrypted body, optional picture captcha, SMS, then cookie mapping)
//
// QR is the primary route and mirrors the zhihu-plus-plus ceremony. When the
// poll hits the 403/40352 network risk-control gate the page surfaces the
// human verification link while polling continues.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

const (
	loginTimeout   = 10 * time.Minute
	qrPollInterval = 3 * time.Second
	qrExpireAfter  = 120 * time.Second
	successHoldMs  = 1500
	// verifyDeadline caps the whole /api/verify chain so the phone login page
	// never waits silently when zhihu stalls a sign_in request (server-side
	// risk control can black-hole the connection for the full 30s client
	// timeout, and the follow-up web steps stack behind it).
	verifyDeadline = 55 * time.Second
)

// login stages surfaced to the page.
const (
	stageIdle    = "idle"
	stageWaiting = "waiting" // QR shown, awaiting scan
	stageScanned = "scanned"
	stageCaptcha = "captcha" // phone flow: picture captcha must be solved
	stageRisk    = "risk"    // network risk-control: human must verify
	stageDone    = "done"
	stageExpired = "expired"
	stageFailed  = "failed"
)

type loginState struct {
	mu      sync.Mutex
	stage   string
	qrURL   string
	qrCode  int
	errMsg  string
	riskURL string
	user    string
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
	if s.riskURL != "" {
		m["risk_url"] = s.riskURL
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
	lnAddr string
	stop   chan struct{}
	state  *loginState
}

// startLoginUI launches the self-contained login server on a random port and
// opens the browser. It returns the local URL. It is safe to call repeatedly;
// a second call while one UI is already running returns the existing URL.
func startLoginUI() (string, error) {
	ui.mu.Lock()
	if ui.active {
		url := "http://" + ui.lnAddr + "/"
		ui.mu.Unlock()
		return url, nil
	}
	ui.active = true
	ui.state = &loginState{stage: stageIdle}
	ui.stop = make(chan struct{})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		ui.active = false
		ui.mu.Unlock()
		return "", fmt.Errorf("zhihu: login listen: %w", err)
	}
	ui.lnAddr = ln.Addr().String()
	url := "http://" + ui.lnAddr + "/"
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
		outcome, err := phoneSendDigits(req.Phone)
		if err != nil {
			ls.set(stageFailed, err.Error())
			writeJSONMap(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if outcome.Captcha {
			ls.mu.Lock()
			ls.stage = stageCaptcha
			ls.mu.Unlock()
			writeJSONMap(w, map[string]any{"ok": false, "captcha": true, "img": outcome.ImageB64})
			return
		}
		writeJSONMap(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/captcha", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Phone     string `json:"phone"`
			InputText string `json:"input_text"`
		}
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Phone == "" || req.InputText == "" {
			http.Error(w, "phone and input_text required", http.StatusBadRequest)
			return
		}
		outcome, err := phoneVerifyCaptcha(req.Phone, req.InputText)
		if err != nil {
			ls.set(stageFailed, err.Error())
			writeJSONMap(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if outcome.Captcha {
			writeJSONMap(w, map[string]any{"ok": false, "captcha": true, "img": outcome.ImageB64})
			return
		}
		ls.mu.Lock()
		ls.stage = stageWaiting
		ls.mu.Unlock()
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
		start := time.Now()
		logf("zhihu: verify: phone=%s start", req.Phone)
		done := make(chan map[string]any, 1)
		go func() {
			token, err := zhihuPhoneSignIn(req.Phone, req.Code)
			if err != nil {
				ls.set(stageFailed, err.Error())
				done <- map[string]any{"ok": false, "error": err.Error()}
				return
			}
			if err := applyPhoneWebSession(token); err != nil {
				ls.set(stageFailed, err.Error())
				done <- map[string]any{"ok": false, "error": err.Error()}
				return
			}
			if err := verifySession(); err != nil {
				ls.set(stageFailed, err.Error())
				done <- map[string]any{"ok": false, "error": err.Error()}
				return
			}
			saveAuth()
			finalizeLogin(ls, "phone")
			done <- map[string]any{"ok": true, "user": AuthUser()}
		}()
		select {
		case m := <-done:
			logf("zhihu: verify: ok=%v elapsed=%s", m["ok"], time.Since(start).Round(time.Millisecond))
			writeJSONMap(w, m)
		case <-time.After(verifyDeadline):
			logf("zhihu: verify: timeout after %s (zhihu side unresponsive)", time.Since(start).Round(time.Millisecond))
			writeJSONMap(w, map[string]any{"ok": false, "error": "登录请求超时(知乎侧无响应),请重试"})
		}
	})

	srv := &http.Server{Handler: mux}
	go func() {
		_ = srv.Serve(ln)
	}()
	go func() {
		select {
		case <-ui.stop:
		case <-time.After(loginTimeout): // hard cap: exit even if abandoned
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		ui.mu.Lock()
		ui.active = false
		ui.mu.Unlock()
	}()
	// Kick off the QR flow immediately; the page just displays it.
	go runQRFlow(ls)

	return url, nil
}

// runQRFlow drives the QR login state machine in the background. It polls the
// official scan_info endpoint: status 0 = waiting for scan, 1 = scanned, and
// a confirmed login surfaces a z_c0 (in the body or Set-Cookie). A 403 code
// 40352 response is the network risk-control gate: the poll continues while
// the page surfaces the /account/unhuman verification link for the user.
func runQRFlow(ls *loginState) {
	token, qrURL, expiresAt, err := qrBegin()
	if err != nil {
		ls.set(stageFailed, err.Error())
		return
	}
	ls.mu.Lock()
	ls.qrURL = qrURL
	ls.stage = stageWaiting
	ls.mu.Unlock()

	deadline := normalizeQrDeadline(expiresAt, time.Now())
	for {
		select {
		case <-ui.stop:
			return
		default:
		}
		if time.Now().After(deadline) {
			if ls.hasRisk() {
				ls.set(stageFailed, "风控未解除(网络环境验证未完成),请刷新页面重试")
			} else {
				ls.set(stageExpired, "")
			}
			return
		}
		body, status, err := qrScanInfo(token)
		if err != nil {
			select {
			case <-ui.stop:
				return
			case <-time.After(qrPollInterval):
			}
			continue
		}
		// Network risk-control gate (403 / code 40352): keep polling, but
		// surface the human verification link once.
		if status == http.StatusForbidden {
			if redirect, ok := scanInfoRiskControl(body); ok {
				ls.setRisk(stageRisk, redirect)
				select {
				case <-ui.stop:
					return
				case <-time.After(qrPollInterval):
				}
				continue
			}
		}
		done, scanned, zc0, failed := parseScanInfo(body)
		if failed != "" {
			ls.set(stageFailed, failed)
			return
		}
		if done {
			if zc0 != "" {
				sess.setZC0(zc0)
			}
			ls.set(stageScanned, "")
			if !sess.hasZ() {
				ls.set(stageFailed, "qr 登录未获得 z_c0")
				return
			}
			if err := verifySession(); err != nil {
				ls.set(stageFailed, err.Error())
				return
			}
			saveAuth()
			finalizeLogin(ls, "qr")
			return
		}
		if scanned {
			ls.set(stageScanned, "")
		}
		select {
		case <-ui.stop:
			return
		case <-time.After(qrPollInterval):
		}
	}
}

// setRisk records the network risk-control state (once) so the page can show
// the verification link while the poll keeps running.
func (s *loginState) setRisk(stage, redirect string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.riskURL != "" {
		return
	}
	s.stage = stage
	s.riskURL = redirect
	if redirect == "" {
		s.riskURL = "https://www.zhihu.com/account/risk_control/"
	}
}

func (s *loginState) hasRisk() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.riskURL != ""
}

// normalizeQrDeadline converts the qrBegin expires_at (epoch seconds, epoch
// millis, or a remaining-TTL) into a wall-clock deadline; absent/absurd values
// fall back to the default poll window (mirrors zhihu-plus-plus).
func normalizeQrDeadline(expiresAt int64, now time.Time) time.Time {
	if expiresAt <= 0 {
		return now.Add(qrExpireAfter)
	}
	nowSec := now.Unix()
	var t time.Time
	switch {
	case expiresAt < nowSec: // TTL semantics when behind "now" in seconds
		if expiresAt <= int64(qrExpireAfter/time.Second) {
			t = now.Add(time.Duration(expiresAt) * time.Second)
		} else {
			t = now.Add(time.Duration(expiresAt) * time.Millisecond)
		}
	case expiresAt < 10_000_000_000: // epoch seconds in the future
		t = time.Unix(expiresAt, 0)
	default: // epoch millis in the future
		t = time.UnixMilli(expiresAt)
	}
	if t.After(now) && t.Before(now.Add(24*time.Hour)) {
		return t
	}
	return now.Add(qrExpireAfter)
}

// finalizeLogin marks the successful login and shuts the UI down shortly after
// so the user can see the success page.
func finalizeLogin(ls *loginState, via string) {
	user := AuthUser()
	ls.mu.Lock()
	ls.stage = stageDone
	ls.user = user
	ls.mu.Unlock()
	logf("zhihu: %s login ok user=%s", via, user)
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

// isDesktop reports whether the process can reach a desktop GUI launcher
// (mirrors outlookgraph oauth.go): darwin/windows always, linux needs
// DISPLAY or WAYLAND_DISPLAY.
func isDesktop() bool {
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	case "linux":
		return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	}
	return false
}

// openURL launches the platform URL opener (outlookgraph parity), returning
// the Start() error so callers can print a manual fallback URL.
func openURL(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// openLoginBrowser opens the login page when a desktop is available; headless
// runs get a manual-URL hint instead (no silent failure).
func openLoginBrowser(url string) {
	if !isDesktop() {
		logf("zhihu: no desktop session; login page (open manually): %s", url)
		return
	}
	if err := openURL(url); err != nil {
		logf("zhihu: open browser failed: %v; open manually: %s", err, url)
		return
	}
	logf("zhihu: browser opened: %s", url)
}

func serveLoginPage(w http.ResponseWriter, _ *loginState) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(loginPageHTML))
}

const loginPageHTML = `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<title>知乎登录 - fedlet</title>
<style>
body{font-family:system-ui,sans-serif;max-width:560px;margin:3rem auto;padding:0 1rem;color:#222}
h1{font-size:1.3rem}
.card{border:1px solid #ddd;border-radius:10px;padding:1.2rem;margin:1rem 0}
.qrimg{width:220px;height:220px;border:1px solid #eee;border-radius:8px}
input{padding:.5rem;width:100%;box-sizing:border-box;margin:.3rem 0;border:1px solid #ccc;border-radius:6px}
button{padding:.55rem 1.4rem;border:0;border-radius:6px;background:#0d69d5;color:#fff;cursor:pointer;margin-top:.5rem}
#state{font-weight:600}
.err{color:#b00020}
pre{white-space:normal;word-break:break-all;background:#f6f6f6;padding:.6rem;border-radius:6px}
</style>
</head>
<body>
<h1>知乎登录(fedlet zhihu)</h1>
<div class="card">
<p id="state">初始化…</p>
<div id="qrbox" style="display:none">
  <img class="qrimg" id="qr" alt="QR">
  <p>用 <b>知乎 App</b> 的「扫一扫」登录;<br>或手机浏览器打开:<pre id="qrl"></pre></p>
</div>
<div id="riskbox" style="display:none">
  <p class="err">检测到网络环境风控,已暂停自动登录。<br>请先完成知乎人工验证后,再回到本页继续:</p>
  <p><a id="risklink" href="#" target="_blank" rel="noopener">打开验证页面</a></p>
</div>
<div id="userbox" style="display:none">登录成功:<b id="user"></b></div>
<div id="failed" class="err" style="display:none"></div>
</div>
<div class="card">
<h2>手机号 + 验证码</h2>
<div id="phoneform">
  <input id="phone" type="tel" placeholder="手机号" autocomplete="tel">
  <button onclick="sendCode()">发送验证码</button>
</div>
<div id="captchaform" style="display:none">
  <img id="captchaimg" alt="验证码" style="max-width:220px;border:1px solid #eee;border-radius:6px">
  <input id="captchainput" type="text" placeholder="图形验证码" autocomplete="off">
  <button onclick="submitCaptcha()">提交验证码并发送短信</button>
</div>
<div id="codeform" style="display:none">
  <input id="code" type="text" placeholder="短信验证码" autocomplete="one-time-code">
  <button id="verifybtn" onclick="doVerify()">登录</button>
</div>
<p id="phonemsg" class="err" style="display:none;white-space:pre-wrap"></p>
</div>
<script>
const $=id=>document.getElementById(id);
async function poll(){
  try{
    const r=await fetch('/api/state');const s=await r.json();
    $('state').textContent={'idle':'准备二维码…','waiting':'等待扫码…','scanned':'已扫码,确认中…','captcha':'请完成图形验证码','risk':'网络环境风控','done':'登录成功','expired':'二维码已过期,请刷新页面','failed':'登录失败'}[s.stage]||s.stage;
    if(s.qr_code_url){$('qrbox').style.display='';$('qr').src=s.qr_code_url;$('qrl').textContent=s.qr_url;}
    if(s.stage==='risk'){ $('riskbox').style.display='';$('risklink').href=s.risk_url||'https://www.zhihu.com/account/risk_control/';}
    if(s.stage==='done'){ $('qrbox').style.display='none';$('userbox').style.display='';$('user').textContent=s.user||'';}
    if(s.stage==='failed'){ $('failed').style.display='';$('failed').textContent=s.error||'未知错误';}
  }catch(e){}
}
function showCaptcha(img){
  $('captchaform').style.display='';
  if(img){$('captchaimg').src=img;}else{$('captchaimg').style.display='none';}
  $('codeform').style.display='none';
}
async function sendCode(){
  const phone=$('phone').value.trim();
  const r=await fetch('/api/phone',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({phone})});
  const j=await r.json();
  if(j.ok){ $('phoneform').style.display='none';$('codeform').style.display='';$('phonemsg').style.display='none';
  } else if(j.captcha){ showCaptcha(j.img);$('phonemsg').style.display='none';
  } else { $('phonemsg').style.display='';$('phonemsg').textContent='发送验证码失败:\n'+j.error; }
}
async function submitCaptcha(){
  const phone=$('phone').value.trim(),input_text=$('captchainput').value.trim();
  if(!input_text){return;}
  const r=await fetch('/api/captcha',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({phone,input_text})});
  const j=await r.json();
  if(j.ok){ $('captchaform').style.display='none';$('codeform').style.display='';$('phonemsg').style.display='none';
  } else if(j.captcha){ $('captchaimg').src=j.img||'';$('phonemsg').style.display='none';
  } else { $('phonemsg').style.display='';$('phonemsg').textContent='发送验证码失败:\n'+j.error; }
}
async function doVerify(){
  const phone=$('phone').value.trim(),code=$('code').value.trim();
  const btn=$('verifybtn');
  btn.disabled=true; $('phonemsg').style.display='';$('phonemsg').textContent='验证中…';
  try{
    const r=await fetch('/api/verify',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({phone,code})});
    const j=await r.json();
    btn.disabled=false;
    if(j.ok){ $('qrbox').style.display='none';$('phoneform').style.display='none';$('codeform').style.display='none';$('captchaform').style.display='none';$('phonemsg').style.display='none';
      $('state').textContent='登录成功';
      $('userbox').style.display='';$('user').textContent=j.user||'';
    } else { $('phonemsg').style.display='';$('phonemsg').textContent='登录失败:\n'+j.error; }
  }catch(e){
    btn.disabled=false;
    $('phonemsg').style.display='';$('phonemsg').textContent='登录失败:\n'+(e&&e.message||'网络错误');
  }
}
poll();setInterval(poll,1500);
</script>
</body>
</html>
`

func logf(format string, args ...any) {
	log.Printf(format, args...)
}
