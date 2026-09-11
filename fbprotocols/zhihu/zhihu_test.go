package zhihu

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// TestParseNotificationsPage covers the SSR /notifications page feed: the
// page embeds initialState.entities.notifications inside js-initialData; items
// are parsed newest-first and rendered from content.verb + actor + preview.
func TestParseNotificationsPage(t *testing.T) {
	const page = `<html><body><script id="js-initialData" type="application/json">` +
		`{"initialState":{"entities":{"notifications":{` +
		`"2079":{"id":2079,"type":"notification","createTime":1788501603,"isRead":true,"mergeCount":1,"content":{` +
		`"verb":"回复了回答下你的评论","actors":[{"name":"王大飞","urlToken":"wdf"}]},` +
		`"target":{"type":"comment","content":"<p>至少30-50年。</p>","target":{` +
		`"excerpt":"问题摘要","question":{"title":"Q题","id":9}}}}` +
		`,"1930":{"id":1930,"type":"notification","createTime":1789000000,"isRead":false,"mergeCount":1,"content":{` +
		`"verb":"关注了你","actors":{"link":"https://www.zhihu.com/people/x","type":"member","urlToken":"x","name":"看不见汽车的地方"}},` +
		`"target":{"type":"people"}}` +
		`}}},"subAppName":"web","spanName":"notifications"}` +
		`</script></body></html>`

	items, err := parseNotificationsPage([]byte(page))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].ID != 1930 || items[1].ID != 2079 {
		t.Fatalf("items not sorted newest-first: %+v", items)
	}
	if got := ActorName(items[0].Actor); got != "看不见汽车的地方" {
		t.Fatalf("object-form actor = %q", got)
	}
	if got := items[0].ActionText; got != "看不见汽车的地方 关注了你" {
		t.Fatalf("follow action_text = %q", got)
	}
	if got := items[1].ActionText; got != "王大飞 回复了回答下你的评论：至少30-50年。" {
		t.Fatalf("comment action_text = %q", got)
	}
	if got := ActorName(items[1].Actor); got != "王大飞" {
		t.Fatalf("list-form actor = %q", got)
	}
	if items[1].IsRead != true || items[1].CreatedTime != 1788501603 {
		t.Fatalf("comment item fields: %+v", items[1])
	}
}

func TestParseNotificationsPageMissingData(t *testing.T) {
	_, err := parseNotificationsPage([]byte("<html><title>消息 - 知乎</title></html>"))
	if !errors.Is(err, errUnverifiable) {
		t.Fatalf("want errUnverifiable, got %v", err)
	}
}

func TestFlexInt64(t *testing.T) {
	var cases = []struct {
		raw  string
		want int64
		ok   bool
	}{
		{`2079`, 2079, true},
		{`"2079207104758409068"`, 2079207104758409068, true},
		{`null`, 0, true},
		{`"abc"`, 0, false},
		{`{}`, 0, false},
	}
	for _, c := range cases {
		var f flexInt64
		err := json.Unmarshal([]byte(c.raw), &f)
		if (err == nil) != c.ok {
			t.Fatalf("Unmarshal(%s) err=%v, want ok=%v", c.raw, err, c.ok)
		}
		if err == nil && int64(f) != c.want {
			t.Fatalf("Unmarshal(%s) = %d, want %d", c.raw, int64(f), c.want)
		}
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
	reLogin.nextAt = time.Time{}
	lastAuthErr = nil
	lastProbeOK = time.Time{}
	meHTMLSheds = 0
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
	if !reLogin.nextAt.IsZero() {
		t.Fatalf("transient error armed the re-login cooldown: nextAt=%s", reLogin.nextAt)
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

func TestEnsureSessionRetriesAfterCooldown(t *testing.T) {
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

	ensureSession(now.Add(30*time.Second), false)
	if uiSpy != 1 {
		t.Fatalf("cooldown should skip the UI, got %d", uiSpy)
	}

	ensureSession(now.Add(reLoginCooldown), false)
	if uiSpy != 2 {
		t.Fatalf("cooldown expiry should re-open the UI, got %d", uiSpy)
	}
}

func TestEnsureSessionForceBypassesCooldown(t *testing.T) {
	resetLoginGateway()
	setupGatewaySession()
	defer resetLoginGateway()

	origProbe, origUI := probeSession, startLoginUIFn
	defer func() { probeSession, startLoginUIFn = origProbe, origUI }()

	uiSpy := 0
	probeSession = func() error { return ErrNotLoggedIn }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	authMu.Lock()
	reLogin.nextAt = now.Add(time.Hour)
	authMu.Unlock()

	ensureSession(now, true)

	if uiSpy != 1 {
		t.Fatalf("force should bypass the cooldown, got %d", uiSpy)
	}
	authMu.Lock()
	defer authMu.Unlock()
	if want := now.Add(reLoginCooldown); !reLogin.nextAt.Equal(want) {
		t.Fatalf("nextAt=%s, want %s", reLogin.nextAt, want)
	}
}

func TestEnsureSessionLiveSkipsUI(t *testing.T) {
	resetLoginGateway()
	setupGatewaySession()
	defer resetLoginGateway()

	origProbe, origUI := probeSession, startLoginUIFn
	defer func() { probeSession, startLoginUIFn = origProbe, origUI }()

	uiSpy := 0
	probeSession = func() error { return nil }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	// Even a force pass must never open the login UI while a live z_c0 probes
	// fine: for an already-logged-in session the QR endpoint would only answer
	// 403 "已登录用户不允许此操作".
	ensureSession(time.Now(), true)

	if uiSpy != 0 {
		t.Fatalf("live session popped the login UI %d times", uiSpy)
	}
	authMu.Lock()
	defer authMu.Unlock()
	if !reLogin.nextAt.IsZero() {
		t.Fatalf("live session armed the re-login cooldown: nextAt=%s", reLogin.nextAt)
	}
	if status := AuthStatus(); status != AuthStatusReady {
		t.Fatalf("live session should stay ready, status=%q", status)
	}
}

func TestEnsureSessionLiveRejectedDropsZ(t *testing.T) {
	resetLoginGateway()
	setupGatewaySession()
	defer resetLoginGateway()

	origProbe, origUI := probeSession, startLoginUIFn
	defer func() { probeSession, startLoginUIFn = origProbe, origUI }()

	uiSpy := 0
	probeSession = func() error { return ErrNotLoggedIn }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	ensureSession(time.Now(), false)

	if uiSpy != 1 {
		t.Fatalf("really-expired session should pop the login UI, got %d", uiSpy)
	}
	sess.mu.Lock()
	z := sess.zC0
	sess.mu.Unlock()
	if z != "" {
		t.Fatalf("rejected z_c0 must be dropped so the login gateway takes over, got %q", z)
	}
}

func TestAlreadyLoggedInRefused(t *testing.T) {
	body := []byte(`{"error":{"code":403,"name":"PERMISSION_ERROR","message":"已登录用户不允许此操作"}}`)
	if !alreadyLoggedInRefused(http.StatusForbidden, body) {
		t.Fatal("403 已登录用户 body must be classified as already-logged-in")
	}
	if alreadyLoggedInRefused(http.StatusForbidden, []byte(`{"error":{"code":403,"name":"RiskControl","message":"风控"}}`)) {
		t.Fatal("generic 403 body must not be classified as already-logged-in")
	}
	if alreadyLoggedInRefused(http.StatusOK, body) {
		t.Fatal("non-403 must not be classified")
	}
}

func TestIsSessionErrUsesErrorsIs(t *testing.T) {
	if !isSessionErr(fmt.Errorf("zhihu: notifications: %w", ErrNotLoggedIn)) {
		t.Fatal("wrapped ErrNotLoggedIn must be recognized as a session error")
	}
	if !isSessionErr(fmt.Errorf("zhihu: hotlist: %w", ErrNotLoggedIn)) {
		t.Fatal("wrapped hotlist ErrNotLoggedIn must be recognized as a session error")
	}
	if isSessionErr(errors.New("boom")) {
		t.Fatal("unrelated error must not be a session error")
	}
}

func TestReLoginResetOnValidSession(t *testing.T) {
	resetLoginGateway()
	setupGatewaySession()
	defer resetLoginGateway()

	origProbe, origUI := probeSession, startLoginUIFn
	defer func() { probeSession, startLoginUIFn = origProbe, origUI }()

	uiSpy := 0
	probeSession = func() error { return nil }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	authMu.Lock()
	reLogin.nextAt = time.Now().Add(time.Hour)
	authMu.Unlock()

	ensureSession(time.Now(), false)

	authMu.Lock()
	defer authMu.Unlock()
	if !reLogin.nextAt.IsZero() {
		t.Fatalf("valid session should clear the re-login cooldown, nextAt=%s", reLogin.nextAt)
	}
	if uiSpy != 0 {
		t.Fatalf("valid session should not pop the login UI, got %d", uiSpy)
	}
}

func TestHandleRoundErrRoutesSessionErrors(t *testing.T) {
	resetLoginGateway()
	defer resetLoginGateway()

	origUI := startLoginUIFn
	defer func() { startLoginUIFn = origUI }()

	uiSpy := 0
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	state := newState()
	handleRoundErr(state, fmt.Errorf("zhihu: notifications: %w", ErrNotLoggedIn))
	if uiSpy != 1 {
		t.Fatalf("session round error should open the login UI, got %d", uiSpy)
	}

	handleRoundErr(state, errors.New("zhihu: upstream down"))
	if uiSpy != 1 {
		t.Fatalf("non-session round error must not open the login UI, got %d", uiSpy)
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

func TestLoginFlowHeaders(t *testing.T) {
	h := loginFlowHeaders(signinReferer, false)
	for _, k := range []string{"User-Agent", "sec-ch-ua", "x-requested-with", "Content-Type", "Origin"} {
		if h[k] == "" {
			t.Fatalf("login headers missing %s", k)
		}
	}
	if h["x-requested-with"] != "fetch" {
		t.Fatalf("x-requested-with = %q", h["x-requested-with"])
	}
	if got := h["Content-Type"]; got != "application/json;charset=UTF-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if h["Referer"] != signinReferer {
		t.Fatalf("non-polling referer = %q", h["Referer"])
	}
	if _, has := h["x-zse-93"]; has {
		t.Fatal("non-polling login headers must NOT carry x-zse-93")
	}

	p := loginFlowHeaders(zhihuSigninURL, true)
	if p["Referer"] != zhihuSigninURL {
		t.Fatalf("polling referer = %q", p["Referer"])
	}
	if p["x-zse-93"] != zse93 {
		t.Fatalf("polling x-zse-93 = %q", p["x-zse-93"])
	}
	if _, has := p["x-zse-96"]; has {
		t.Fatal("login poll must NEVER carry x-zse-96")
	}
	if p["Accept"] != "*/*" {
		t.Fatalf("polling accept = %q", p["Accept"])
	}
}

func TestParseQrBegin(t *testing.T) {
	tok, link, expires, err := parseQrBegin([]byte(`{"token":"T0k3n","url":"https://zhihu.com/q?t=1","expires_at":1789021559}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tok != "T0k3n" || link != "https://zhihu.com/q?t=1" || expires != 1789021559 {
		t.Fatalf("got %q/%q/%d", tok, link, expires)
	}

	if _, _, _, err := parseQrBegin([]byte(`{"link":"https://zhihu.com/q?t=2","token":"T2"}`)); err != nil {
		t.Fatalf("link+token form: %v", err)
	}
	tokL, linkL, _, err := parseQrBegin([]byte(`{"token":"T3","url":"https://zhihu.com/q?t=url","link":"https://zhihu.com/q?t=link"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tokL != "T3" || linkL != "https://zhihu.com/q?t=link" {
		t.Fatalf("link must win over url, got %q", linkL)
	}

	if _, _, _, err := parseQrBegin([]byte(`{}`)); err == nil {
		t.Fatal("missing token must error")
	}
	if _, _, _, err := parseQrBegin([]byte(`not json`)); err == nil {
		t.Fatal("garbage must error")
	}
}

// TestUIAddrUsable pins the "invalid login link" guard: only a real bound
// loopback address with a non-zero port is acceptable, so the http://host:0/
// style links can never be produced or reused as a live UI URL.
func TestUIAddrUsable(t *testing.T) {
	cases := []struct {
		addr string
		ok   bool
	}{
		{"", false},
		{"127.0.0.1:0", false},
		{"127.0.0.1", false},
		{":33099", false},
		{"localhost:33099", false},
		{"127.0.0.1:33099", true},
	}
	for _, c := range cases {
		if got := uiAddrUsable(c.addr); got != c.ok {
			t.Fatalf("uiAddrUsable(%q)=%v want %v", c.addr, got, c.ok)
		}
	}
}

func TestScanInfoRiskControl(t *testing.T) {
	// 40352 need_login with a redirect target.
	rd, ok := scanInfoRiskControl([]byte(`{"error":{"code":40352,"need_login":true,"redirect":"https://www.zhihu.com/account/unhuman?x=1"}}`))
	if !ok || rd != "https://www.zhihu.com/account/unhuman?x=1" {
		t.Fatalf("risk body not detected: ok=%v rd=%q", ok, rd)
	}
	// top-level code 40352 (older shape).
	rd, ok = scanInfoRiskControl([]byte(`{"code":40352,"redirect":"/account/unhuman"}`))
	if !ok || rd != "/account/unhuman" {
		t.Fatalf("top-level risk not detected: ok=%v rd=%q", ok, rd)
	}
	// genuine poll responses must not be flagged.
	for _, body := range []string{`{"status":0}`, `{"status":1,"scanned":true}`, `{"status":1,"user_id":1}`, `not json`} {
		if _, ok := scanInfoRiskControl([]byte(body)); ok {
			t.Fatalf("false positive risk for %s", body)
		}
	}
}

func TestNormalizeQrDeadline(t *testing.T) {
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	within := func(t, want time.Time) bool { return t.Sub(want) < time.Second }

	// absent -> default window.
	if got := normalizeQrDeadline(0, now); !within(got, now.Add(qrExpireAfter)) {
		t.Fatalf("zero expiry -> %s", got)
	}
	// future epoch seconds.
	if got := normalizeQrDeadline(now.Add(90*time.Second).Unix(), now); !within(got, now.Add(90*time.Second)) {
		t.Fatalf("epoch-seconds expiry -> %s", got)
	}
	// future epoch millis.
	if got := normalizeQrDeadline(now.Add(90*time.Second).UnixMilli(), now); !within(got, now.Add(90*time.Second)) {
		t.Fatalf("epoch-millis expiry -> %s", got)
	}
	// TTL seconds (expires_at is a remaining-TTL, i.e. smaller than now's seconds).
	if got := normalizeQrDeadline(90, now); !within(got, now.Add(90*time.Second)) {
		t.Fatalf("ttl-seconds expiry -> %s", got)
	}
	// absurd future value -> clamped back to the default window.
	if got := normalizeQrDeadline(now.Add(80*time.Hour).UnixMilli(), now); !within(got, now.Add(qrExpireAfter)) {
		t.Fatalf("absurd expiry -> %s", got)
	}
}

// ---- invalid auth-file handling and HTML/401 classification ----

func resetSessionCreds() {
	sess.mu.Lock()
	sess.zC0 = ""
	sess.xsrf = ""
	sess.zap = ""
	sess.qC1 = ""
	sess.capsion = ""
	sess.capSession = ""
	sess.user = ""
	sess.status = AuthStatusEmpty
	sess.mu.Unlock()
}

func resetRateGate() {
	rateMu.Lock()
	rateUntil = time.Time{}
	rateBackoff = 0
	rateMu.Unlock()
}

// stubTransport lets tests answer HTTP without a network.
type stubTransport func(*http.Request) (*http.Response, error)

func (f stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// stubClient serves targetBody/status for one exact URL and fallback for every
// other request (warmup calls, login helpers, ...).
func stubClient(target string, targetBody []byte, status int, fallback []byte) *http.Client {
	return &http.Client{Transport: stubTransport(func(r *http.Request) (*http.Response, error) {
		body, code := fallback, http.StatusOK
		if target != "" && r.URL.String() == target {
			body, code = targetBody, status
		}
		return &http.Response{
			StatusCode: code,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    r,
		}, nil
	})}
}

func TestApplyAuthFile(t *testing.T) {
	resetSessionCreds()
	defer resetSessionCreds()

	// Invalid files must never replay the rejected z_c0/_xsrf: the credential
	// is left empty and the login gateway takes over (status=invalid).
	applyAuthFile(authFileJSON{DC0: "d0|1|x", ZC0: "zc0-test", XSRF: "xsrf-x", User: "nobody", Status: AuthStatusInvalid})
	sess.mu.RLock()
	if sess.zC0 != "" || sess.xsrf != "" {
		t.Fatalf("invalid file replayed z_c0/xsrf: %q/%q", sess.zC0, sess.xsrf)
	}
	if sess.status != AuthStatusInvalid {
		t.Fatalf("status=%q, want invalid", sess.status)
	}
	if sess.dC0 != "d0|1|x" || sess.user != "nobody" {
		t.Fatalf("d_c0/user should be kept: %q/%q", sess.dC0, sess.user)
	}
	sess.mu.RUnlock()

	// Valid files keep the session.
	applyAuthFile(authFileJSON{ZC0: "zc0-ok", XSRF: "xsrf-ok", Status: AuthStatusReady})
	sess.mu.RLock()
	if sess.zC0 != "zc0-ok" || sess.xsrf != "xsrf-ok" || sess.status != AuthStatusReady {
		t.Fatalf("valid file not applied: %q/%q/%q", sess.zC0, sess.xsrf, sess.status)
	}
	sess.mu.RUnlock()

	// No z_c0 -> empty runtime session even if a stray status text persists.
	applyAuthFile(authFileJSON{Status: "weird"})
	sess.mu.RLock()
	if sess.zC0 != "" || sess.status != "weird" {
		t.Fatalf("empty session misapplied: %q/%q", sess.zC0, sess.status)
	}
	sess.mu.RUnlock()
}

func TestLooksHTML(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"", false},
		{"{}", false},
		{`{"a":1}`, false},
		{"[]", false},
		{"<html>", true},
		{"<!doctype html>", true},
		{"  <div>x</div>", true},
		{"\t<b>hi</b>", true},
	}
	for _, tc := range cases {
		if got := looksHTML([]byte(tc.body)); got != tc.want {
			t.Fatalf("looksHTML(%q)=%v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestGetRawHTML200ClassifiesUnverifiable(t *testing.T) {
	resetSessionCreds()
	resetLoginGateway()
	resetRateGate()
	defer resetSessionCreds()

	sess.mu.Lock()
	sess.zC0 = "zc0-test"
	sess.status = AuthStatusReady
	sess.mu.Unlock()

	oldHC := hc
	hc = stubClient(meURL, []byte("<!doctype html><html>bot check</html>"), http.StatusOK, []byte("{}"))
	defer func() { hc = oldHC }()

	_, status, err := getRaw(http.MethodGet, meURL, nil, true)
	if !errors.Is(err, errUnverifiable) {
		t.Fatalf("err=%v, want errUnverifiable", err)
	}
	if !strings.Contains(err.Error(), meURL) {
		t.Fatalf("err=%q must carry the requested url %q", err, meURL)
	}
	if status != http.StatusOK {
		t.Fatalf("status=%d, want 200", status)
	}
	rateMu.Lock()
	defer rateMu.Unlock()
	if !rateUntil.After(time.Now()) {
		t.Fatal("HTML 200 should arm the rate gate")
	}
}

func TestGetRaw401NeedAuthMarksInvalid(t *testing.T) {
	resetSessionCreds()
	resetLoginGateway()
	resetRateGate()
	defer resetSessionCreds()

	dir := t.TempDir()
	oldPath := authFilePathFn
	authFilePathFn = func() string { return dir + "/zhihu-auth.json" }
	defer func() { authFilePathFn = oldPath }()

	sess.mu.Lock()
	sess.zC0 = "zc0-test"
	sess.status = AuthStatusReady
	sess.mu.Unlock()

	oldHC := hc
	hc = stubClient(meURL, []byte("{}"), http.StatusUnauthorized, []byte("{}"))
	defer func() { hc = oldHC }()

	_, status, err := getRaw(http.MethodGet, meURL, nil, true)
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("err=%v, want ErrNotLoggedIn", err)
	}
	if status != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", status)
	}
	if got := AuthStatus(); got != AuthStatusInvalid {
		t.Fatalf("status=%q, want invalid", got)
	}
}

func TestGetRaw401AnonymousDoesNotMarkInvalid(t *testing.T) {
	resetSessionCreds()
	resetLoginGateway()
	resetRateGate()
	defer resetSessionCreds()

	sess.mu.Lock()
	sess.status = AuthStatusReady
	sess.mu.Unlock()

	u := qrBaseURL + "/tok/scan_info"
	oldHC := hc
	hc = stubClient(u, []byte("{}"), http.StatusUnauthorized, []byte("{}"))
	defer func() { hc = oldHC }()

	// needAuth=false: a 401 on a login-helper endpoint must not kill the session.
	_, _, err := getRaw(http.MethodGet, u, nil, false)
	if errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("anonymous 401 must stay a plain error, got ErrNotLoggedIn")
	}
	if got := AuthStatus(); got != AuthStatusReady {
		t.Fatalf("session status was clobbered by anonymous 401: %q", got)
	}
	if last := LastAuthErr(); last != nil {
		t.Fatalf("anonymous 401 recorded an auth error: %v", last)
	}
}

func TestVerifySessionHTMLShedTransientKeepsReady(t *testing.T) {
	resetSessionCreds()
	resetLoginGateway()
	resetRateGate()
	defer resetSessionCreds()

	dir := t.TempDir()
	oldPath := authFilePathFn
	authFilePathFn = func() string { return dir + "/zhihu-auth.json" }
	defer func() { authFilePathFn = oldPath }()

	sess.mu.Lock()
	sess.zC0 = "zc0-test"
	sess.status = AuthStatusReady
	sess.mu.Unlock()

	oldHC := hc
	hc = stubClient(meURL, []byte("<html>login required</html>"), http.StatusOK, []byte("{}"))
	defer func() { hc = oldHC }()

	// A single shed is ambiguous and must not demote a ready session.
	err := verifySession()
	if !errors.Is(err, errUnverifiable) {
		t.Fatalf("err=%v, want transient errUnverifiable", err)
	}
	if got := AuthStatus(); got != AuthStatusReady {
		t.Fatalf("single HTML shed must keep the session ready, got %q", got)
	}
}

func TestVerifySessionHTMLShedThresholdDemotes(t *testing.T) {
	resetSessionCreds()
	resetLoginGateway()
	resetRateGate()
	defer resetSessionCreds()

	dir := t.TempDir()
	oldPath := authFilePathFn
	authFilePathFn = func() string { return dir + "/zhihu-auth.json" }
	defer func() { authFilePathFn = oldPath }()

	sess.mu.Lock()
	sess.zC0 = "zc0-test"
	sess.status = AuthStatusReady
	sess.mu.Unlock()

	oldHC := hc
	hc = stubClient(meURL, []byte("<html>login required</html>"), http.StatusOK, []byte("{}"))
	defer func() { hc = oldHC }()

	// (meHTMLShedLimit-1) sheds stay transient; the limit-th shed demotes.
	for i := 1; i < meHTMLShedLimit; i++ {
		if err := verifySession(); !errors.Is(err, errUnverifiable) {
			t.Fatalf("shed #%d err=%v, want transient errUnverifiable", i, err)
		}
	}
	if got := AuthStatus(); got != AuthStatusReady {
		t.Fatalf("shed #%d must keep the session ready, got %q", meHTMLShedLimit-1, got)
	}

	err := verifySession()
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("limit shed err=%v, want ErrNotLoggedIn", err)
	}
	if got := AuthStatus(); got != AuthStatusInvalid {
		t.Fatalf("status=%q, want invalid", got)
	}
}

func TestHandleRoundErrUnverifiableDemotes(t *testing.T) {
	resetLoginGateway()
	setupGatewaySession()
	defer resetLoginGateway()

	origProbe, origUI := probeSession, startLoginUIFn
	defer func() { probeSession, startLoginUIFn = origProbe, origUI }()

	uiSpy := 0
	probeSession = func() error { return ErrNotLoggedIn }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	state := newState()
	handleRoundErr(state, fmt.Errorf("zhihu: notifications: %w (http 200: <html>...)", errUnverifiable))

	if got := AuthStatus(); got != AuthStatusInvalid {
		t.Fatalf("status=%q, want invalid (rejected probe demotes)", got)
	}
	if uiSpy != 1 {
		t.Fatalf("HTML round error on a dead session should open the login UI, got %d", uiSpy)
	}
	sess.mu.Lock()
	z := sess.zC0
	sess.mu.Unlock()
	if z != "" {
		t.Fatalf("rejected z_c0 must be dropped, got %q", z)
	}
}

func TestHandleRoundErrUnverifiableLiveKeeps(t *testing.T) {
	resetLoginGateway()
	setupGatewaySession()
	defer resetLoginGateway()

	origProbe, origUI := probeSession, startLoginUIFn
	defer func() { probeSession, startLoginUIFn = origProbe, origUI }()

	uiSpy := 0
	probeSession = func() error { return nil }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	state := newState()
	handleRoundErr(state, fmt.Errorf("zhihu: notifications: %w (http 200: <html>...)", errUnverifiable))

	if uiSpy != 0 {
		t.Fatalf("risk-control shed on a live session must not open the login UI, got %d", uiSpy)
	}
	authMu.Lock()
	defer authMu.Unlock()
	if !reLogin.nextAt.IsZero() {
		t.Fatalf("live session armed the re-login cooldown: nextAt=%s", reLogin.nextAt)
	}
	if status := AuthStatus(); status != AuthStatusReady {
		t.Fatalf("live session should stay ready, status=%q", status)
	}
}

func TestReloginDue(t *testing.T) {
	resetLoginGateway()
	defer resetLoginGateway()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	if !reloginDue(now) {
		t.Fatal("zero nextAt should be due")
	}
	authMu.Lock()
	reLogin.nextAt = now.Add(30 * time.Second)
	authMu.Unlock()
	if reloginDue(now) {
		t.Fatal("cooldown not elapsed should not be due")
	}
	if !reloginDue(now.Add(31 * time.Second)) {
		t.Fatal("elapsed cooldown should be due")
	}
}
