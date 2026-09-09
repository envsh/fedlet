package zhihu

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSignSource(t *testing.T) {
	u := "https://www.zhihu.com/api/v3/feed/topstory/hot-lists/total?limit=50&mobile=true"
	// Like the reference implementation, the raw text after the host (including
	// the query string) feeds the signature.
	want := "101_3_3.0+/api/v3/feed/topstory/hot-lists/total?limit=50&mobile=true+Atest|1|ab"
	if got := buildSignSource(u, "Atest|1|ab", ""); got != want {
		t.Fatalf("buildSignSource = %q, want %q", got, want)
	}
	if got := extractPathname("https://www.zhihu.com/api/v4/me"); got != "/api/v4/me" {
		t.Fatalf("extractPathname = %q", got)
	}
}

func TestSignDeterministicAndFormat(t *testing.T) {
	h1 := signZhihuRequest("https://www.zhihu.com/api/v4/me", "Adc0|1|ab", "")
	h2 := signZhihuRequest("https://www.zhihu.com/api/v4/me", "Adc0|1|ab", "")
	if h1["x-zse-96"] != h2["x-zse-96"] {
		t.Fatalf("signature not deterministic: %q vs %q", h1["x-zse-96"], h2["x-zse-96"])
	}
	if h1["x-zse-93"] != "101_3_3.0" {
		t.Fatalf("x-zse-93 = %q", h1["x-zse-93"])
	}
	if !strings.HasPrefix(h1["x-zse-96"], "2.0_") {
		t.Fatalf("x-zse-96 prefix missing: %q", h1["x-zse-96"])
	}
	if len(h1["x-zse-96"]) < 10 {
		t.Fatalf("x-zse-96 suspiciously short: %q", h1["x-zse-96"])
	}
	// different d_c0 must change the signature
	h3 := signZhihuRequest("https://www.zhihu.com/api/v4/me", "Adc0|2|cc", "")
	if h1["x-zse-96"] == h3["x-zse-96"] {
		t.Fatalf("signature did not depend on d_c0")
	}
}

func TestEncodeOutputAlphabet(t *testing.T) {
	for _, in := range []string{"", "a", "abc", "0123456789abcdefABCDEF", "中文路径"} {
		out := encryptZseV4(in)
		if len(out)%4 != 0 {
			t.Fatalf("encryptZseV4(%q) output length %d not multiple of 4", in, len(out))
		}
		for _, r := range out {
			if !strings.ContainsRune(signAlphabet, r) {
				t.Fatalf("encryptZseV4(%q) has char %q not in alphabet", in, r)
			}
		}
	}
}

func TestHotlistParse(t *testing.T) {
	const js = `{"fresh_text":"刚刚更新","data":[
		{"id":"1","type":"hot_list_feed",
		 "target":{"id":609686334,"type":"question","title":"如何评价?",
		           "excerpt":"补充说明","detail_text":"1亿热度 · 讨论 5 万",
		           "answer_count":123,"author":{"name":"甲"}}},
		{"id":"2","type":"hot_list_feed",
		 "target":{"id":1,"type":"pin","detail_text":"想法摘要"}}
	]}`
	var r HotlistResp
	if err := json.Unmarshal([]byte(js), &r); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(r.Data) != 2 || r.FreshText != "刚刚更新" {
		t.Fatalf("unexpected parse: %+v", r)
	}
	if got := HotlistTitle(r.Data[0].Target); got != "如何评价?" {
		t.Fatalf("title = %q", got)
	}
	if got := HotlistDetail(r.Data[0].Target); got != "1亿热度 · 讨论 5 万" {
		t.Fatalf("detail = %q", got)
	}
	if got := HotlistTitle(r.Data[1].Target); got != "想法摘要" {
		t.Fatalf("pin title = %q", got)
	}
}

func TestHotlistNewShape(t *testing.T) {
	const js = `{"data":[{"id":"3","type":"hot_list_feed","target":{
		"title_area":{"text":"新结构标题"},
		"excerpt_area":{"text":"摘要文字"},
		"metrics_area":{"text":"2.3亿热度 · 1.8万回答"},
		"link":{"url":"https://www.zhihu.com/question/12345"}}},
	{"id":"4","type":"hot_list_feed","target":{
		"metrics_area":{"text":"393万热度"}}}
	]}`
	var r HotlistResp
	if err := json.Unmarshal([]byte(js), &r); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(r.Data) != 2 {
		t.Fatalf("unexpected parse: %+v", r)
	}
	if got := HotlistTitle(r.Data[0].Target); got != "新结构标题" {
		t.Fatalf("title_area title = %q", got)
	}
	if got := HotlistDetail(r.Data[0].Target); got != "摘要文字" {
		t.Fatalf("excerpt_area detail = %q", got)
	}
	if got := HotlistLink(r.Data[0].Target); got != "https://www.zhihu.com/question/12345" {
		t.Fatalf("link = %q", got)
	}
	if got := HotlistDetail(r.Data[1].Target); got != "393万热度" {
		t.Fatalf("metrics_area detail = %q", got)
	}
	if got := HotlistLink(r.Data[1].Target); got != "" {
		t.Fatalf("link should be empty, got %q", got)
	}
}

func TestHotlistFetchRequiresLogin(t *testing.T) {
	// With no z_c0 session the hot list is gated before any API call.
	if sess.hasZ() {
		t.Skip("session already authenticated")
	}
	_, err := FetchHotlist()
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("FetchHotlist without session = %v, want ErrNotLoggedIn", err)
	}
}

func TestNotifyParse(t *testing.T) {
	const js = `{"data":[{"id":998877,"verb":"upvote",
		"action_text":"赞同了你的回答",
		"actor":{"name":"somebody","url_token":"u1"},
		"target":{"id":1},"created_time":1780000000,
		"is_read":false,"type":"action"}]}`
	var r NotifyResp
	if err := json.Unmarshal([]byte(js), &r); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(r.Data) != 1 || r.Data[0].ID != 998877 {
		t.Fatalf("unexpected parse: %+v", r)
	}
	if got := ActorName(r.Data[0].Actor); got != "somebody" {
		t.Fatalf("actor = %q", got)
	}
}

func TestStatePrune(t *testing.T) {
	s := newState()
	now := time.Now()
	old := now.Add(-73 * time.Hour).Unix()
	s.Hotlist["a"] = old
	s.Hotlist["b"] = now.Unix()
	s.Notifications["1"] = old
	s.Notifications["2"] = now.Unix()
	pruneState(s, now)
	if _, ok := s.Hotlist["a"]; ok {
		t.Fatal("hotlist entry not pruned")
	}
	if _, ok := s.Notifications["1"]; ok {
		t.Fatal("notification entry not pruned")
	}
	if _, ok := s.Hotlist["b"]; !ok {
		t.Fatal("fresh hotlist entry pruned")
	}
	if _, ok := s.Notifications["2"]; !ok {
		t.Fatal("fresh notification entry pruned")
	}
}

func TestAuthStatusInitial(t *testing.T) {
	// Never touches disk; just asserts the anonymous starting state.
	if got := AuthStatus(); got != AuthStatusEmpty {
		t.Fatalf("initial AuthStatus = %q, want empty", got)
	}
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	if sess.dC0 == "" || !strings.Contains(sess.dC0, "|") {
		t.Fatalf("generated d_c0 invalid: %q", sess.dC0)
	}
	if sess.zC0 != "" {
		t.Fatalf("fresh session unexpectedly has z_c0")
	}
}

// ---- ensureSession (login gateway) unit tests ----

func resetLoginGateway() {
	authMu.Lock()
	reLogin.attempts = 0
	reLogin.nextAt = time.Time{}
	lastAuthErr = nil
	authMu.Unlock()
	ui.mu.Lock()
	ui.active = false
	ui.mu.Unlock()
}

// setupGatewaySession marks the session ready with a synthetic z_c0 so the
// probe seam decides the outcome; no network is touched.
func setupGatewaySession() {
	sess.mu.Lock()
	sess.zC0 = "zc0-test"
	sess.status = AuthStatusReady
	sess.mu.Unlock()
}

func TestEnsureSessionTransientKeepsReady(t *testing.T) {
	resetLoginGateway()
	setupGatewaySession()
	defer resetLoginGateway()

	origProbe, origUI := probeSession, startLoginUIFn
	defer func() { probeSession, startLoginUIFn = origProbe, origUI }()

	uiSpy := 0
	probeSession = func() error { return errors.New("net down") }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	ensureSession(time.Now(), true)

	if uiSpy != 0 {
		t.Fatalf("transient error popped the login UI %d times", uiSpy)
	}
	authMu.Lock()
	defer authMu.Unlock()
	if reLogin.attempts != 0 {
		t.Fatalf("transient error consumed re-login budget: attempts=%d", reLogin.attempts)
	}
	if status := AuthStatus(); status != AuthStatusReady {
		t.Fatalf("ready session should be kept, status=%q", status)
	}
}

func TestEnsureSessionExpiredStartsLogin(t *testing.T) {
	resetLoginGateway()
	setupGatewaySession()
	defer resetLoginGateway()

	origProbe, origUI := probeSession, startLoginUIFn
	defer func() { probeSession, startLoginUIFn = origProbe, origUI }()

	uiSpy := 0
	probeSession = func() error { return ErrNotLoggedIn }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	ensureSession(now, false)

	if uiSpy != 1 {
		t.Fatalf("expired session should pop the login UI, got %d", uiSpy)
	}
	authMu.Lock()
	defer authMu.Unlock()
	if reLogin.attempts != 1 {
		t.Fatalf("attempts=%d, want 1", reLogin.attempts)
	}
	if want := now.Add(reLoginCooldown); !reLogin.nextAt.Equal(want) {
		t.Fatalf("nextAt=%s, want %s", reLogin.nextAt, want)
	}
}

func TestEnsureSessionCooldownSkipsUI(t *testing.T) {
	resetLoginGateway()
	setupGatewaySession()
	defer resetLoginGateway()

	origProbe, origUI := probeSession, startLoginUIFn
	defer func() { probeSession, startLoginUIFn = origProbe, origUI }()

	uiSpy := 0
	probeSession = func() error { return ErrNotLoggedIn }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	ensureSession(now, false)
	ensureSession(now.Add(1*time.Minute), false)

	if uiSpy != 1 {
		t.Fatalf("cooldown should skip the second UI, got %d", uiSpy)
	}
}

func TestEnsureSessionAttemptCap(t *testing.T) {
	resetLoginGateway()
	setupGatewaySession()
	defer resetLoginGateway()

	origProbe, origUI := probeSession, startLoginUIFn
	defer func() { probeSession, startLoginUIFn = origProbe, origUI }()

	uiSpy := 0
	probeSession = func() error { return ErrNotLoggedIn }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	authMu.Lock()
	reLogin.attempts = reLoginMaxAttempts
	authMu.Unlock()

	ensureSession(time.Now(), false)

	if uiSpy != 0 {
		t.Fatalf("attempt cap should stop the UI, got %d", uiSpy)
	}
}

func TestEnsureSessionForceBypassesBudget(t *testing.T) {
	resetLoginGateway()
	setupGatewaySession()
	defer resetLoginGateway()

	origProbe, origUI := probeSession, startLoginUIFn
	defer func() { probeSession, startLoginUIFn = origProbe, origUI }()

	uiSpy := 0
	probeSession = func() error { return ErrNotLoggedIn }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	authMu.Lock()
	reLogin.attempts = reLoginMaxAttempts
	authMu.Unlock()

	ensureSession(time.Now(), true)

	if uiSpy != 1 {
		t.Fatalf("force should bypass the budget, got %d", uiSpy)
	}
}

func TestScanInfoParse(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		done    bool
		scanned bool
		zc0     string
		failed  string
	}{
		{"waiting", `{"status":0}`, false, false, "", ""},
		{"scanned", `{"status":1,"qr_id":"x"}`, false, true, "", ""},
		{"access token", `{"status":1,"access_token":"tok123","user_id":123456}`, true, true, "", ""},
		{"user id number only", `{"status":1,"user_id":123456}`, true, true, "", ""},
		{"z_c0 in body", `{"status":1,"z_c0":"ZAc0x==|}Zhong"}`, true, true, "ZAc0x==|}Zhong", ""},
		{"cookie field", `{"status":1,"cookie":"z_c0=ZUpd0|zhi~; dash=1"}`, true, true, "ZUpd0|zhi~", ""},
		{"cookies field", `{"status":1,"cookies":"z_c0=ZLR0_/abc;; _xsrf=ab"}`, true, true, "ZLR0_/abc", ""},
		{"login_status ok", `{"status":1,"login_status":"CONFIRMED"}`, true, true, "", ""},
		{"success bool", `{"status":1,"success":true}`, true, true, "", ""},
		{"logged in bool", `{"status":1,"logged_in":true}`, true, true, "", ""},
		{"garbage", `not json`, false, false, "", ""},
		{"empty login_status", `{"status":0,"login_status":""}`, false, false, "", ""},
		{"error code", `{"error":{"code":10003,"message":"请求被拦截"}}`, false, false, "", "请求被拦截"},
		{"error name", `{"error":{"name":"ParamError"}}`, false, false, "", "ParamError"},
		{"error top-level", `{"code":103,"message":"oops"}`, false, false, "", "oops"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			done, scanned, zc0, failed := parseScanInfo([]byte(tc.body))
			if failed != tc.failed {
				t.Fatalf("failed = %q, want %q", failed, tc.failed)
			}
			if done != tc.done {
				t.Fatalf("done = %v, want %v", done, tc.done)
			}
			if scanned != tc.scanned {
				t.Fatalf("scanned = %v, want %v", scanned, tc.scanned)
			}
			if zc0 != tc.zc0 {
				t.Fatalf("zc0 = %q, want %q", zc0, tc.zc0)
			}
		})
	}
}

func TestScanInfoCookieString(t *testing.T) {
	// z_c0 is picked out of a semicolon-separated cookie payload; missing or
	// malformed values come back empty.
	m := map[string]any{"cookie": "z_c0=ZC0=|a|b;;; _xsrf=abc; color=blue"}
	if got := scanInfoCookieString(m, "cookie"); got != "ZC0=|a|b" {
		t.Fatalf("cookie parse = %q", got)
	}
	if got := scanInfoCookieString(map[string]any{"cookie": "color=blue"}, "cookie"); got != "" {
		t.Fatalf("no z_c0 should be empty, got %q", got)
	}
	if got := scanInfoCookieString(map[string]any{}, "cookie"); got != "" {
		t.Fatalf("empty map should be empty, got %q", got)
	}
}
