package zhihu

// Self-contained login UI.
//
// When a session is needed the protocol starts a throwaway HTTP server on a
// random 127.0.0.1 port (never localhost, to avoid ::1/dual-stack surprises),
// opens the system browser on it, and shuts itself down once the login
// finishes (or times out). The page offers both login routes:
//
//   - QR scan (auto-started: get token, poll scan/confirm, capture z_c0)
//   - phone + verify code (send code, then submit the code)
//
// The phone endpoints mirror the zhihu-plus web flow; exact field layout is
// 待实测 and their responses are handled tolerantly.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
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
	qrCode int
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
		if err := phoneSendCode(req.Phone); err != nil {
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
		if err := phoneLogin(req.Phone, req.Code); err != nil {
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

	openBrowser(url)
	return url, nil
}

// runQRFlow drives the QR login state machine in the background. It polls the
// official scan_info endpoint: status 0 = waiting for scan, 1 = scanned, and
// a confirmed login surfaces a z_c0 (in the body or Set-Cookie).
func runQRFlow(ls *loginState) {
	token, qrURL, err := qrBegin()
	if err != nil {
		ls.set(stageFailed, err.Error())
		return
	}
	ls.mu.Lock()
	ls.qrURL = qrURL
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
		body, _, err := qrScanInfo(token)
		if err != nil {
			select {
			case <-ui.stop:
				return
			case <-time.After(qrPollInterval):
			}
			continue
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

func openBrowser(url string) {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{url}
	case "windows":
		name, args = "cmd", []string{"/c", "start", "", url}
	default:
		name, args = "xdg-open", []string{url}
	}
	if name == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, name, args...).Start(); err != nil {
		logf("zhihu: open browser %s failed: %v", url, err)
	}
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
<div id="userbox" style="display:none">登录成功:<b id="user"></b></div>
<div id="failed" class="err" style="display:none"></div>
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
<script>
const $=id=>document.getElementById(id);
async function poll(){
  try{
    const r=await fetch('/api/state');const s=await r.json();
    $('state').textContent={'idle':'准备二维码…','waiting':'等待扫码…','scanned':'已扫码,确认中…','done':'登录成功','expired':'二维码已过期,请刷新页面','failed':'登录失败'}[s.stage]||s.stage;
    if(s.qr_code_url){$('qrbox').style.display='';$('qr').src=s.qr_code_url;$('qrl').textContent=s.qr_url;}
    if(s.stage==='done'){ $('qrbox').style.display='none';$('userbox').style.display='';$('user').textContent=s.user||'';}
    if(s.stage==='failed'){ $('failed').style.display='';$('failed').textContent=s.error||'未知错误';}
  }catch(e){}
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
</script>
</body>
</html>
`

func logf(format string, args ...any) {
	log.Printf(format, args...)
}
