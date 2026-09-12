package xhs

// Behavioural tests for the orchestrator pieces that do not need the network:
// hot list / notification parsing, dedupe state pruning, the risk interaction
// state machine, cookie-store semantics, and the rate gate. Signing golden
// vectors live in golden_test.go / xpos_test.go.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

func jsonUnmarshalStr(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

func mustMap(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := jsonUnmarshalStr(s, &m); err != nil {
		t.Fatalf("json fixture: %v", err)
	}
	return m
}

func TestParseHotlistItems(t *testing.T) {
	out := mustMap(t, `{
		"items": [
			{
				"id": "5ff0e6c70000000001008f9a",
				"type": "note",
				"note_card": {
					"id": "64a1b2c3000000",
					"display_title": "周末山野徒步",
					"interact_info": {"liked_count": 12345, "collected_count": 300, "comment_count": 12}
				},
				"user": {"user_id": "u1", "nickname": "旅人"}
			},
			{"id": "", "type": "ad"},
			{"id": "note-b", "type": "note"}
		]
	}`)
	items := parseHotlistItems(out)
	if len(items) != 3 {
		t.Fatalf("len = %d want 3", len(items))
	}
	first := &items[0]
	if first.NoteID != "64a1b2c3000000" {
		t.Errorf("NoteID = %q (wants note_card.id override)", first.NoteID)
	}
	if got := HotlistTitle(first); got != "周末山野徒步" {
		t.Errorf("title = %q", got)
	}
	if got := HotlistLink(first); got != "https://www.xiaohongshu.com/explore/64a1b2c3000000" {
		t.Errorf("link = %q", got)
	}
	if got := HotlistDetail(first); got == "" || got == "👍 0 ⭐ 0 💬 0" {
		t.Errorf("detail = %q", got)
	}
	if items[2].NoteID != "note-b" {
		t.Errorf("items[2].NoteID = %q", items[2].NoteID)
	}
}

func TestParseNotifications(t *testing.T) {
	out := mustMap(t, `{
		"xhs_item": [
			{
				"id": "n1",
				"user": {"user_id": "u1", "nickname": "小明"},
				"text": "来啦来啦",
				"create_time": 1710000000,
				"unread": 1,
				"note": {"id": "note-1", "title": "标题A"}
			},
			{
				"id": "n2",
				"from_user": {"user_id": "u2", "nickname": "小红"},
				"text": "👍",
				"created_at": 1710000001,
				"read": true,
				"target": {"id": "note-2", "display_title": "标题B"}
			},
			{"id": ""}
		]
	}`)
	items := parseYouKind(out, KindMentions)
	if len(items) != 2 {
		t.Fatalf("len = %d want 2 (empty no-id item skipped)", len(items))
	}
	first := items[0]
	if first.ActorName != "小明" || first.NoteID != "note-1" || !first.Unread {
		t.Errorf("first = %+v", first)
	}
	if first.CreateTime != 1710000000 {
		t.Errorf("create_time = %d", first.CreateTime)
	}
	second := items[1]
	if second.ActorName != "小红" || second.NoteTitle != "标题B" || second.Unread {
		t.Errorf("second = %+v", second)
	}
	if second.ID != "n2" || second.Kind != KindMentions {
		t.Errorf("kind/id = %s/%s", second.Kind, second.ID)
	}
}

func TestPruneState(t *testing.T) {
	s := &xhsState{
		Hotlist:       map[string]int64{"old": time.Now().Add(-100 * time.Hour).Unix()},
		Notifications: map[string]int64{"new": time.Now().Unix()},
	}
	pruneState(s, time.Now())
	if _, ok := s.Hotlist["old"]; ok {
		t.Error("old hotlist entry should be pruned")
	}
	if _, ok := s.Notifications["new"]; !ok {
		t.Error("fresh notification entry should survive")
	}
}

func TestStateRoundTrip(t *testing.T) {
	path := t.TempDir() + "/state.json"
	s := newState()
	s.Hotlist["a"] = 1000
	saveState(path, s)
	got := loadState(path)
	if got.Hotlist["a"] != 1000 {
		t.Errorf("hotlist a = %d", got.Hotlist["a"])
	}
	if got.Notifications == nil {
		t.Error("Notifications must not be nil after load")
	}
}

func TestRiskStateMachine(t *testing.T) {
	done := make(chan bool, 1)
	go func() {
		ok, jar := relayRisk(riskSignal{
			status: 461,
			headers: http.Header{
				"Verifytype": []string{"124"},
				"Verifyuuid": []string{"abc-def"},
			},
		}, map[string]string{"WebSession": "s", "webId": "w1"})
		if jar != nil {
			t.Errorf("relayRisk returned a jar on manual-verify success")
		}
		done <- ok
	}()

	var ch *riskChallenge
	deadline := time.Now().Add(2 * time.Second)
	for {
		var active bool
		ch, _, _, active = pendingRisk()
		if active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pendingRisk never armed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ch == nil || ch.verifyUUID != "abc-def" || ch.verifyType != "124" {
		t.Fatalf("challenge = %+v", ch)
	}

	clearRisk()
	select {
	case ok := <-done:
		if !ok {
			t.Error("relayRisk should return ok=true after clearRisk")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relayRisk did not unblock after clearRisk")
	}
}

func TestRiskURLBuilding(t *testing.T) {
	ch := riskChallenge{status: 471, verifyType: "124", verifyUUID: "uu-id"}
	u := secondaryVerifyURLFor("rid-1", ch, "web-id-1")
	want := "https://www.xiaohongshu.com/web-login/qrcode-transfer?rid=rid-1&verifyUuid=uu-id&verifyBiz=471&verifyType=124&webid=web-id-1"
	if u != want {
		t.Errorf("qr url:\n got %s\nwant %s", u, want)
	}
}

func TestRiskChallengeParse(t *testing.T) {
	h := http.Header{}
	h.Set("VERIFYTYPE", "102")
	h.Set("verifyuuID", "x")
	c := parseRiskChallenge(471, h)
	if c.status != 471 || c.verifyType != "102" || c.verifyUUID != "x" {
		t.Errorf("challenge = %+v", c)
	}
	if got := c.recordLog(); got == "" {
		t.Error("recordLog empty")
	}
}

func TestCookieStoreLoadCapture(t *testing.T) {
	cs := newCookieStore()
	cs.load(map[string]string{"a1": "abc", "webId": "w"})
	if cs.get("a1") != "abc" {
		t.Errorf("load a1 = %q", cs.get("a1"))
	}
	h := http.Header{
		"Set-Cookie": []string{"name=x; Path=/; HttpOnly", "other=y"},
	}
	cs.captureCookies(h)
	if cs.get("name") != "x" || cs.get("other") != "y" {
		t.Errorf("captured: %v", cs.all())
	}
	all := cs.all()
	if all["a1"] != "abc" || all["webId"] != "w" {
		t.Errorf("all() merged badly: %v", all)
	}
}

func TestNumAsIntParities(t *testing.T) {
	if v, ok := numAsInt(float64(5)); !ok || v != 5 {
		t.Errorf("float64: %d/%v", v, ok)
	}
	if v, ok := numAsInt("42"); !ok || v != 42 {
		t.Errorf("string: %d/%v", v, ok)
	}
	if v, ok := numAsInt(int64(7)); !ok || v != 7 {
		t.Errorf("int64: %d/%v", v, ok)
	}
	if _, ok := numAsInt(map[string]any{}); ok {
		t.Error("map should not convert")
	}
}

func TestEnsureGuestSessionBypass(t *testing.T) {
	oldProbe := probeSession
	oldUI := startLoginUIFn
	defer func() {
		probeSession = oldProbe
		startLoginUIFn = oldUI
	}()
	uiSpy := 0
	probeSession = func() error { return nil }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	s := client()
	s.opts.auth.mu.Lock()
	old := s.opts.auth.status
	s.opts.auth.status = authStatusGuest
	s.opts.auth.mu.Unlock()
	// AuthStatus() derives from the WebSession cookie, which a real guest
	// session (login/activate) also has.
	s.cookies.set(xhsWebSessionName, "guest-session")
	defer func() {
		s.opts.auth.mu.Lock()
		s.opts.auth.status = old
		s.opts.auth.mu.Unlock()
		s.cookies.set(xhsWebSessionName, "")
	}()

	reLoginMu.Lock()
	reLogin.nextAt = time.Time{}
	reLoginMu.Unlock()

	ensureSession(time.Now(), true)
	if uiSpy != 0 {
		t.Errorf("guest session should not schedule login, ui=%d", uiSpy)
	}
	reLoginMu.Lock()
	defer reLoginMu.Unlock()
	if !reLogin.nextAt.IsZero() {
		t.Errorf("guest session must leave the re-login cooldown cleared, nextAt=%s", reLogin.nextAt)
	}
}

func TestIsSessionErrUsesErrorsIs(t *testing.T) {
	if !isSessionErr(fmt.Errorf("xhs: hotlist: %w", ErrNotLoggedIn)) {
		t.Fatal("wrapped ErrNotLoggedIn must be recognized as a session error")
	}
	if isSessionErr(errors.New("boom")) {
		t.Fatal("unrelated error must not be a session error")
	}
}

func TestIsSessionErrMatchesRejectedText(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("xhs: api code -100: 未登录"), true},
		{errors.New("xhs: unread_count: 未登录"), true},
		{errors.New("xhs: http 403"), true},
		{errors.New("xhs: bad json: invalid character '<'"), false},
		{errors.New("xhs: upstream down"), false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := isSessionErr(tc.err); got != tc.want {
			t.Fatalf("isSessionErr(%v)=%v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestFeedAllowed(t *testing.T) {
	if !feedAllowed(AuthStatusReady, true) {
		t.Error("notify must run when ready")
	}
	if feedAllowed(authStatusGuest, true) {
		t.Error("notify must not run on a guest session")
	}
	if feedAllowed(AuthStatusEmpty, true) {
		t.Error("notify must not run without a session")
	}
	if !feedAllowed(AuthStatusReady, false) {
		t.Error("hot must run when ready")
	}
	if !feedAllowed(authStatusGuest, false) {
		t.Error("hot must run on a guest session")
	}
	if feedAllowed(AuthStatusInvalid, false) {
		t.Error("hot must not run on an invalid session")
	}
	if feedAllowed(AuthStatusEmpty, false) {
		t.Error("hot must not run without a session")
	}
}

func TestReloginDue(t *testing.T) {
	reLoginMu.Lock()
	reLogin.nextAt = time.Time{}
	reLoginMu.Unlock()
	defer func() {
		reLoginMu.Lock()
		reLogin.nextAt = time.Time{}
		reLoginMu.Unlock()
	}()

	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	if !reloginDue(now) {
		t.Fatal("zero nextAt should be due")
	}
	reLoginMu.Lock()
	reLogin.nextAt = now.Add(30 * time.Second)
	reLoginMu.Unlock()
	if reloginDue(now) {
		t.Fatal("cooldown not elapsed should not be due")
	}
	if !reloginDue(now.Add(31 * time.Second)) {
		t.Fatal("elapsed cooldown should be due")
	}
}

func TestLooksHTML(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"", false},
		{"{}", false},
		{`{"code":0}`, false},
		{"<html>", true},
		{"<!doctype html>", true},
		{"  <div>x</div>", true},
	}
	for _, tc := range cases {
		if got := looksHTML([]byte(tc.body)); got != tc.want {
			t.Fatalf("looksHTML(%q)=%v, want %v", tc.body, got, tc.want)
		}
	}
}

// stubRT answers HTTP without a network.
type stubRT func(*http.Request) (*http.Response, error)

func (f stubRT) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestVerifySessionRiskKeepsStatus(t *testing.T) {
	old := session
	sessionMu.Lock()
	session = old
	if session == nil {
		session = newXHSClient(clientOptions{})
	}
	s := session
	sessionMu.Unlock()
	defer func() {
		sessionMu.Lock()
		session = old
		sessionMu.Unlock()
	}()

	s.http = &http.Client{Transport: stubRT(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: httpStatusRisk,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
			Request:    r,
		}, nil
	})}
	s.cookies.set(xhsWebSessionName, "dead")
	s.opts.auth.mu.Lock()
	s.opts.auth.status = AuthStatusReady
	s.opts.auth.mu.Unlock()

	err := verifySession()
	if !errors.Is(err, ErrRiskControl) {
		t.Fatalf("err=%v, want ErrRiskControl", err)
	}
	if got := AuthStatus(); got != AuthStatusReady {
		t.Fatalf("a 461 challenge must keep the session status, got %q", got)
	}
}

func TestMarkSessionInvalidClearsCookie(t *testing.T) {
	old := session
	sessionMu.Lock()
	session = nil
	sessionMu.Unlock()
	defer func() {
		sessionMu.Lock()
		session = old
		sessionMu.Unlock()
	}()

	c := client()
	c.cookies.set(xhsWebSessionName, "dead")
	authMu.Lock()
	lastAuthErr = nil
	authMu.Unlock()

	markSessionInvalid(errors.New("xhs: rejected"))

	if got := c.cookies.get(xhsWebSessionName); got != "" {
		t.Fatalf("dead web_session must be dropped from the jar, got %q", got)
	}
	if got := AuthStatus(); got != AuthStatusEmpty {
		t.Fatalf("AuthStatus=%q, want empty (cookie dropped)", got)
	}
	if got := LastAuthErr(); got == nil {
		t.Fatal("rejection reason should be recorded")
	}
}

// TestUIAddrUsable pins the "invalid login link" guard: only a real bound
// loopback address with a non-zero port is acceptable.
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
