package zhihu

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

// ---- daily bulletin ----

const testDailyLatest = `{"date":"20260910","stories":[
{"id":9792490,"title":"请问哪些植物毛茸茸的？","url":"https://daily.zhihu.com/story/9792490","hint":"小星有鬼 · 2 分钟阅读","images":["https://pic1.zhimg.com/x.jpg"],"type":0},
{"id":9792481,"title":"瞎扯 · 如何正确地吐槽","url":"https://daily.zhihu.com/story/9792481","hint":"VOL.3986","images":[],"type":0}],
"top_stories":[{"id":9792490,"title":"请问哪些植物毛茸茸的？","url":"https://daily.zhihu.com/story/9792490","hint":"小星有鬼 · 2 分钟阅读","images":["https://pic1.zhimg.com/x.jpg"],"type":0}]}`

const testDailyStoryDetail = `{"id":9792481,"title":"瞎扯 · 如何正确地吐槽","image":"https://pic1.zhimg.com/img.jpg","images":["https://pic1.zhimg.com/x.jpg"],"share_url":"http://daily.zhihu.com/story/9792481","body":"<p>吐槽要讲究方法。</p>"}`

func TestDailyFetchLatestParse(t *testing.T) {
	oldHC := hc
	hc = stubClient(dailyLatestURL, []byte(testDailyLatest), http.StatusOK, []byte("{}"))
	defer func() { hc = oldHC }()

	r, err := FetchDailyLatest()
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if r.Date != "20260910" || len(r.Stories) != 2 || len(r.TopStories) != 1 {
		t.Fatalf("bad latest: %+v", r)
	}
	if r.Stories[0].ID != 9792490 || r.Stories[0].Title != "请问哪些植物毛茸茸的？" {
		t.Fatalf("bad story: %+v", r.Stories[0])
	}
	if len(r.Stories[0].Images) != 1 || r.Stories[1].ID != 9792481 {
		t.Fatalf("bad images/order: %+v", r.Stories)
	}
}

func TestDailyFetchStoryParse(t *testing.T) {
	u := "https://daily.zhihu.com/api/4/story/9792481"
	oldHC := hc
	hc = stubClient(u, []byte(testDailyStoryDetail), http.StatusOK, []byte("{}"))
	defer func() { hc = oldHC }()

	d, err := FetchDailyStory(9792481)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if d.ID != 9792481 || d.Title == "" || !strings.Contains(d.Body, "吐槽") {
		t.Fatalf("bad detail: %+v", d)
	}
}

func TestDailySummaryStripHTML(t *testing.T) {
	s := stripHTMLSummary(`<p>你好&amp;世界</p><p><img src="x"/></p><strong> 加粗 </strong>  多空格`)
	if s != "你好&世界 加粗 多空格" {
		t.Fatalf("strip = %q", s)
	}
	long := strings.Repeat("一二三四五", 100)
	if got := stripHTMLSummary("<p>" + long + "</p>"); len([]rune(got)) > dailySummaryLen+3 {
		t.Fatalf("summary too long: %d runes", len([]rune(got)))
	}
}

func TestDailyRoundFirstSyncSeedsNoPublish(t *testing.T) {
	oldHC := hc
	hc = stubClient(dailyLatestURL, []byte(testDailyLatest), http.StatusOK, []byte("{}"))
	defer func() { hc = oldHC }()
	spy := 0
	oldPub := pubfn_
	pubfn_ = func(any) error { spy++; return nil }
	defer func() { pubfn_ = oldPub }()

	state := newState()
	dailyRound(state)

	if spy != 0 {
		t.Fatalf("first sync published %d items", spy)
	}
	if len(state.Daily) != 2 {
		t.Fatalf("seeded %d ids, want 2", len(state.Daily))
	}
	if _, ok := state.Daily["9792490"]; !ok {
		t.Fatalf("missing seed id 9792490")
	}
}

func TestDailyRoundPublishesNewOnly(t *testing.T) {
	oldHC := hc
	hc = &http.Client{Transport: stubTransport(func(r *http.Request) (*http.Response, error) {
		body := []byte("{}")
		switch r.URL.String() {
		case dailyLatestURL:
			body = []byte(testDailyLatest)
		case "https://daily.zhihu.com/api/4/story/9792481":
			body = []byte(testDailyStoryDetail)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})}
	defer func() { hc = oldHC }()

	var got []map[string]any
	oldPub := pubfn_
	pubfn_ = func(v any) error { got = append(got, v.(map[string]any)); return nil }
	defer func() { pubfn_ = oldPub }()

	state := newState()
	state.Daily["9792490"] = time.Now().Unix()
	dailyRound(state)

	if len(got) != 1 {
		t.Fatalf("published %d, want 1", len(got))
	}
	p := got[0]
	if p["kind"] != "daily" || p["id"] != int64(9792481) {
		t.Fatalf("bad payload: %+v", p)
	}
	if p["title"] != "瞎扯 · 如何正确地吐槽" || p["date"] != "20260910" {
		t.Fatalf("bad title/date: %+v", p)
	}
	if p["image"] != "https://pic1.zhimg.com/img.jpg" {
		t.Fatalf("bad image fallback: %+v", p)
	}
	desc, ok := p["description"].(string)
	if !ok || !strings.Contains(desc, "吐槽要讲究方法") {
		t.Fatalf("bad description: %+v", desc)
	}
	if _, seen := state.Daily["9792481"]; !seen {
		t.Fatalf("new id not seeded after publish")
	}

	got = nil
	dailyRound(state)
	if len(got) != 0 {
		t.Fatalf("re-published %d after dedupe", len(got))
	}
}

func TestDailyStateNilSafeLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zhihu-state.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	s := loadState(path)
	if s.Daily == nil {
		t.Fatal("loadState did not nil-guard Daily")
	}
}

func TestDailyStatePrune(t *testing.T) {
	s := newState()
	s.Daily["old"] = time.Now().Add(-(dedupeExpiry + time.Hour)).Unix()
	s.Daily["fresh"] = time.Now().Unix()
	pruneState(s, time.Now())
	if _, ok := s.Daily["old"]; ok {
		t.Fatal("stale daily id survived prune")
	}
	if s.Daily["fresh"] == 0 {
		t.Fatal("fresh daily id pruned")
	}
}

func TestStartEnablesDaily(t *testing.T) {
	setStartConfig(false, false, 0, 0)
	muClient.Lock()
	defer muClient.Unlock()
	if !dailyOn {
		t.Fatal("daily should auto-enable on Start")
	}
	if dailyInt != defaultDailyInterval {
		t.Fatalf("daily interval=%s, want %s", dailyInt, defaultDailyInterval)
	}
}

// ---- recommend feed (pull-only layer) ----

const testRecommendFeed = `{"paging":{"is_end":false,"next":"https://www.zhihu.com/api/v3/feed/topstory/recommend?desktop=true&limit=20&after_id=20","previous":""},"fresh_text":"刷新看看新的好内容","data":[
{"id":1001,"type":"answer","created_time":1750000000,"target":{"id":5555,"type":"answer","url":"https://www.zhihu.com/question/333/answer/5555","excerpt":"马的视野很广。","question":{"id":333,"title":"除了奔跑很快，马还有哪些很厉害的技能？"},"author":{"name":"桔大"},"voteup_count":1234,"comment_count":56,"content":"<p>马的视野很广，擅长听声辨位。</p>"}},
{"id":1002,"type":"article","created_time":0,"target":{"id":7777,"type":"article","url":"https://zhuanlan.zhihu.com/p/7777","title":"一篇长文","excerpt":"摘要文字。","author":{"name":"阿伟"},"voteup_count":99,"comment_count":3,"content":"<p>正文内容。</p>"}},
{"id":1003,"type":"zvideo","created_time":0,"target":{"id":8888,"type":"zvideo","title":"视频标题","author":{"name":"UP"},"content":"<p>视频描述文本。</p>","comment_count":1}},
{"id":1004,"type":"question","created_time":0,"target":{"id":999,"type":"question","title":"问题标题","excerpt":"问题描述。","author":{"name":"qauthor"}}},
{"id":1005,"type":"pin","created_time":0,"target":{"id":1111,"type":"pin","author":{"name":"pinner"},"content":"<p>想法内容。</p>","comment_count":7}},
{"id":1006,"type":"ad","created_time":0,"target":{}}
]}`

func TestFetchRecommendParse(t *testing.T) {
	resetSessionCreds()
	setupGatewaySession()
	defer resetSessionCreds()

	u := "https://www.zhihu.com/api/v3/feed/topstory/recommend?desktop=true&limit=20"
	oldHC := hc
	hc = stubClient(u, []byte(testRecommendFeed), http.StatusOK, []byte("{}"))
	defer func() { hc = oldHC }()

	r, err := FetchRecommend(20)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(r.Data) != 6 {
		t.Fatalf("items=%d, want 6", len(r.Data))
	}
	if r.Paging.IsEnd || !strings.Contains(r.Paging.Next, "after_id=20") {
		t.Fatalf("bad paging: %+v", r.Paging)
	}
	if r.FreshText == "" {
		t.Fatal("fresh_text not parsed")
	}
	if r.Data[0].ID != 1001 || r.Data[0].Type != "answer" || r.Data[0].CreatedTime != 1750000000 {
		t.Fatalf("bad first item: %+v", r.Data[0])
	}
}

func TestFetchRecommendDefaultLimit(t *testing.T) {
	resetSessionCreds()
	setupGatewaySession()
	defer resetSessionCreds()

	gotURL := ""
	oldHC := hc
	hc = &http.Client{Transport: stubTransport(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader([]byte(`{"data":[]}`))), Request: r}, nil
	})}
	defer func() { hc = oldHC }()

	if _, err := FetchRecommend(0); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(gotURL, "limit=20") {
		t.Fatalf("default limit not applied: %s", gotURL)
	}
}

func TestFetchRecommendNeedsSession(t *testing.T) {
	resetSessionCreds()
	defer resetSessionCreds()

	calls := 0
	oldHC := hc
	hc = &http.Client{Transport: stubTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusInternalServerError, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(nil)), Request: r}, nil
	})}
	defer func() { hc = oldHC }()

	if _, err := FetchRecommend(2); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("want ErrNotLoggedIn, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("made %d network calls without a session", calls)
	}
}

func TestRecommendViewRich(t *testing.T) {
	var r struct {
		Data []RecommendItem `json:"data"`
	}
	if err := json.Unmarshal([]byte(testRecommendFeed), &r); err != nil {
		t.Fatal(err)
	}

	ans := RecommendView(&r.Data[0])
	if ans.ID != 5555 || ans.Kind != "answer" || ans.Title != "除了奔跑很快，马还有哪些很厉害的技能？" {
		t.Fatalf("answer: %+v", ans)
	}
	if ans.Author != "桔大" || ans.VoteupCount != 1234 || ans.CommentCount != 56 {
		t.Fatalf("answer metrics: %+v", ans)
	}
	if ans.Summary != "马的视野很广，擅长听声辨位。" {
		t.Fatalf("answer summary: %q", ans.Summary)
	}
	if ans.URL != "https://www.zhihu.com/question/333/answer/5555" {
		t.Fatalf("answer url: %q", ans.URL)
	}

	art := RecommendView(&r.Data[1])
	if art.Kind != "article" || art.Title != "一篇长文" || art.Summary != "摘要文字。" || art.Author != "阿伟" || art.VoteupCount != 99 {
		t.Fatalf("article: %+v", art)
	}
	if art.Excerpt != "摘要文字。" {
		t.Fatalf("article excerpt: %q", art.Excerpt)
	}

	zv := RecommendView(&r.Data[2])
	if zv.Kind != "zvideo" || zv.Title != "视频标题" || zv.Summary != "视频描述文本。" || zv.URL != "" {
		t.Fatalf("zvideo: %+v", zv)
	}

	q := RecommendView(&r.Data[3])
	if q.Kind != "question" || q.Title != "问题标题" || q.Summary != "问题描述。" {
		t.Fatalf("question: %+v", q)
	}

	p := RecommendView(&r.Data[4])
	if p.Kind != "pin" || p.Title != "想法内容。" || p.CommentCount != 7 {
		t.Fatalf("pin: %+v", p)
	}

	ad := RecommendView(&r.Data[5])
	if ad.Kind != "ad" || ad.ID != 1006 || ad.Title != "" {
		t.Fatalf("ad: %+v", ad)
	}
}

func TestRecommendViewAnswerLinkFallback(t *testing.T) {
	var it RecommendItem
	if err := json.Unmarshal([]byte(`{"id":10,"type":"answer","target":{"id":20,"type":"answer","question":{"id":30},"author":{"name":"x"}}}`), &it); err != nil {
		t.Fatal(err)
	}
	v := RecommendView(&it)
	want := "https://www.zhihu.com/question/30/answer/20"
	if v.URL != want {
		t.Fatalf("fallback url=%q, want %q", v.URL, want)
	}
}
