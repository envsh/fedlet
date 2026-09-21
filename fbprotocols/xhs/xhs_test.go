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
	"net/url"
	"strings"
	"sync"
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
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:33099/", nil }

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

func TestGuestWithNeedRealLoginOpensUI(t *testing.T) {
	oldProbe := probeSession
	oldUI := startLoginUIFn
	defer func() {
		probeSession = oldProbe
		startLoginUIFn = oldUI
	}()
	uiSpy := 0
	probeSession = func() error { return nil }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:33099/", nil }

	s := client()
	s.opts.auth.mu.Lock()
	old := s.opts.auth.status
	s.opts.auth.status = authStatusGuest
	s.opts.auth.mu.Unlock()
	s.cookies.set(xhsWebSessionName, "guest-session")
	defer func() {
		s.opts.auth.mu.Lock()
		s.opts.auth.status = old
		s.opts.auth.mu.Unlock()
		s.cookies.set(xhsWebSessionName, "")
	}()

	muClient.Lock()
	oldNotify, oldCollect := notifyOn, collectOn
	notifyOn = true
	collectOn = true
	muClient.Unlock()
	defer func() {
		muClient.Lock()
		notifyOn, collectOn = oldNotify, oldCollect
		muClient.Unlock()
	}()

	reLoginMu.Lock()
	reLogin.nextAt = time.Time{}
	reLoginMu.Unlock()

	ensureSession(time.Now(), true)
	if uiSpy == 0 {
		t.Error("guest session with a real login required must schedule the login UI")
	}
	reLoginMu.Lock()
	defer reLoginMu.Unlock()
	if reLogin.nextAt.IsZero() {
		t.Error("scheduled login must set the re-login cooldown")
	}
}

func TestReadyDowngradedToGuestWithNeedOpensUI(t *testing.T) {
	oldProbe := probeSession
	oldUI := startLoginUIFn
	defer func() {
		probeSession = oldProbe
		startLoginUIFn = oldUI
	}()
	uiSpy := 0
	// probeSession mirrors verifySession downgrading a stored "ready" session
	// (auth.json guest is loaded as ready) back to guest, returning nil.
	probeSession = func() error {
		s := client()
		s.opts.auth.mu.Lock()
		s.opts.auth.status = authStatusGuest
		s.opts.auth.mu.Unlock()
		return nil
	}
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:33099/", nil }

	s := client()
	s.opts.auth.mu.Lock()
	old := s.opts.auth.status
	s.opts.auth.status = AuthStatusReady
	s.opts.auth.mu.Unlock()
	s.cookies.set(xhsWebSessionName, "guest-session")
	defer func() {
		s.opts.auth.mu.Lock()
		s.opts.auth.status = old
		s.opts.auth.mu.Unlock()
		s.cookies.set(xhsWebSessionName, "")
	}()

	muClient.Lock()
	oldNotify, oldCollect := notifyOn, collectOn
	notifyOn = true
	collectOn = true
	muClient.Unlock()
	defer func() {
		muClient.Lock()
		notifyOn, collectOn = oldNotify, oldCollect
		muClient.Unlock()
	}()

	reLoginMu.Lock()
	reLogin.nextAt = time.Time{}
	reLoginMu.Unlock()

	ensureSession(time.Now(), true)
	if uiSpy == 0 {
		t.Error("ready session downgraded to guest with a real login required must open the login UI")
	}
	reLoginMu.Lock()
	defer reLoginMu.Unlock()
	if reLogin.nextAt.IsZero() {
		t.Error("scheduled login must set the re-login cooldown")
	}
}

func TestEmptyWithGuestOnlyMintsNoUI(t *testing.T) {
	oldUI := startLoginUIFn
	oldGuest := ensureGuest
	defer func() {
		startLoginUIFn = oldUI
		ensureGuest = oldGuest
	}()
	uiSpy := 0
	guestSpy := 0
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:33099/", nil }
	ensureGuest = func() { guestSpy++ }

	s := client()
	s.opts.auth.mu.Lock()
	old := s.opts.auth.status
	s.opts.auth.mu.Unlock()
	s.cookies.set(xhsWebSessionName, "") // AuthStatus() becomes empty without web_session
	defer func() {
		s.opts.auth.mu.Lock()
		s.opts.auth.status = old
		s.opts.auth.mu.Unlock()
		s.cookies.set(xhsWebSessionName, "")
	}()

	muClient.Lock()
	oldNotify, oldCollect := notifyOn, collectOn
	notifyOn = false
	collectOn = false
	muClient.Unlock()
	defer func() {
		muClient.Lock()
		notifyOn, collectOn = oldNotify, oldCollect
		muClient.Unlock()
	}()

	reLoginMu.Lock()
	reLogin.nextAt = time.Time{}
	reLoginMu.Unlock()

	ensureSession(time.Now(), true)
	if guestSpy == 0 {
		t.Error("empty status without a real-login feed must mint a guest session")
	}
	if uiSpy != 0 {
		t.Errorf("guest-only run must not open the login UI, ui=%d", uiSpy)
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

// urlRecorder is a RoundTripper that captures outgoing request URLs and Cookie
// headers.
type urlRecorder struct {
	mu      sync.Mutex
	urls    []*url.URL
	cookies []string
	bodies  []string
}

func (r *urlRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.urls = append(r.urls, req.URL)
	r.cookies = append(r.cookies, req.Header.Get("Cookie"))
	var bodyText string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		bodyText = string(b)
		req.Body = io.NopCloser(bytes.NewReader(b))
	}
	r.bodies = append(r.bodies, bodyText)
	r.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(`{"code":0,"data":{}}`)),
		Request:    req,
	}, nil
}

// TestSignedURLCarriesRealPath is a regression test: getJSONOpts/postJSONOpts
// must forward the caller's uri (and GET params) into the signed request.
// Dropping them sent every request to the bare host root (nginx HTML reply,
// "bad json", throttling).
func TestSignedURLCarriesRealPath(t *testing.T) {
	rec := &urlRecorder{}
	c := newXHSClient(clientOptions{})
	c.http = &http.Client{Transport: rec}
	c.cookies.set("a1", generateA1())

	if _, _, err := c.getJSON("/api/sns/web/v2/user/me", []kvParam{{key: "num", val: "50"}}); err != nil {
		t.Fatalf("getJSON: %v", err)
	}
	if _, _, err := c.postJSONOpts(loginCodeURL, nil, phoneSignOpts()); err != nil {
		t.Fatalf("postJSONOpts: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.urls) != 2 {
		t.Fatalf("got %d requests, want 2", len(rec.urls))
	}
	getURL := rec.urls[0]
	if getURL.Path != "/api/sns/web/v2/user/me" {
		t.Errorf("GET path = %q want /api/sns/web/v2/user/me", getURL.Path)
	}
	if got := getURL.Query().Get("num"); got != "50" {
		t.Errorf("GET num = %q want 50", got)
	}
	if postURL := rec.urls[1]; postURL.Path != loginCodeURL {
		t.Errorf("POST path = %q want %s", postURL.Path, loginCodeURL)
	}
}

// TestQueryOrderMatchesSignedContent is a regression test for HTTP 406 on
// multi-param GETs: the wire query must be byte-identical (same order, same
// escaping) to the content the signature hashes. url.Values.Encode re-sorts
// keys alphabetically, which broke send_code/check_code/qr_status.
func TestQueryOrderMatchesSignedContent(t *testing.T) {
	params := []kvParam{{key: "phone", val: "13800138000"}, {key: "zone", val: "86"}, {key: "type", val: "login"}}
	const uri = "/api/sns/web/v2/login/send_code"
	const want = "phone=13800138000&zone=86&type=login"

	wire := buildURL(xhsAPIHost+uri, params)
	if !strings.HasSuffix(wire, "?"+want) {
		t.Errorf("wire URL = %q, want suffix ?%s (kvParam order preserved)", wire, want)
	}
	if !strings.HasPrefix(wire, xhsAPIHost) {
		t.Errorf("wire URL = %q, want prefix %q", wire, xhsAPIHost)
	}
	if signed := buildContentString("GET", uri, params, ""); signed != uri+"?"+want {
		t.Errorf("signed content = %q, want %q", signed, uri+"?"+want)
	}
}

// TestSignedQueryOrderIsRaw verifies a real signed GET request carries the
// query exactly as signed (order preserved, not re-sorted by encoding).
func TestSignedQueryOrderIsRaw(t *testing.T) {
	rec := &urlRecorder{}
	c := newXHSClient(clientOptions{})
	c.http = &http.Client{Transport: rec}
	c.cookies.set("a1", generateA1())

	params := []kvParam{{key: "phone", val: "13800138000"}, {key: "zone", val: "86"}, {key: "type", val: "login"}}
	if _, _, err := c.getJSONOpts("/api/sns/web/v2/login/send_code", params, phoneSignOpts()); err != nil {
		t.Fatalf("getJSONOpts: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.urls) != 1 {
		t.Fatalf("got %d requests, want 1", len(rec.urls))
	}
	u := rec.urls[0]
	if u.Path != "/api/sns/web/v2/login/send_code" {
		t.Fatalf("path = %q, want /api/sns/web/v2/login/send_code", u.Path)
	}
	if got := u.RawQuery; got != "phone=13800138000&zone=86&type=login" {
		t.Errorf("RawQuery = %q, want %q (order must match the signed content)", got, "phone=13800138000&zone=86&type=login")
	}
}

// TestPhoneLoginEndpointVersions pins the phone-login endpoint versions against
// the current PC client (Spider_XHS 2026-09): send_code is v2, check_code is
// v1 and login/code is v2 (verified live 2026-09-21). The earlier v1
// expectation — and the "v2 is rejected with -1" hypothesis behind its comment —
// was dropped once the live SMS login succeeded on the v2 endpoint with the
// body-only wire pinned by TestPhoneLoginWireMatchesReference.
func TestPhoneLoginEndpointVersions(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"sendCodeURL", sendCodeURL, "/api/sns/web/v2/login/send_code"},
		{"checkCodeURL", checkCodeURL, "/api/sns/web/v1/login/check_code"},
		{"loginCodeURL", loginCodeURL, "/api/sns/web/v2/login/code"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestPhoneLoginOmitsSessionCookies pins that the phone-login family strips the
// session identity cookies (guest web_session otherwise makes login/code answer
// -104 "您当前登录的账号没有权限访问") while every other request keeps them.
func TestPhoneLoginOmitsSessionCookies(t *testing.T) {
	rec := &urlRecorder{}
	c := newXHSClient(clientOptions{})
	c.http = &http.Client{Transport: rec}
	c.cookies.set("a1", generateA1())
	c.cookies.set("webId", "w1")
	c.cookies.set(xhsWebSessionName, "guest-session")
	c.cookies.set("web_session", "guest-session")
	c.cookies.set(webSessionSecCookie, "sec")

	params := []kvParam{{key: "phone", val: "13800138000"}, {key: "zone", val: "86"}, {key: "type", val: "login"}}
	if _, _, err := c.getJSONOpts(sendCodeURL, params, phoneSignOpts()); err != nil {
		t.Fatalf("phone getJSONOpts: %v", err)
	}
	if _, _, err := c.getJSON("/api/sns/web/v2/user/me", nil); err != nil {
		t.Fatalf("plain getJSON: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.cookies) != 2 {
		t.Fatalf("got %d requests, want 2", len(rec.cookies))
	}
	phone := rec.cookies[0]
	if !strings.Contains(phone, "a1=") {
		t.Errorf("phone request Cookie = %q, want a1 kept", phone)
	}
	for _, sess := range []string{"web_session", xhsWebSessionName, webSessionSecCookie} {
		if strings.Contains(phone, sess+"=") {
			t.Errorf("phone request Cookie contains session cookie %q: %q", sess, phone)
		}
	}
	if plain := rec.cookies[1]; !strings.Contains(plain, xhsWebSessionName+"=") {
		t.Errorf("plain request Cookie = %q, want session cookies kept", plain)
	}
}

// TestPhoneLoginWireMatchesReference pins the login/code POST exactly as the
// current PC client (Spider_XHS 2026-09) sends it: POST to /api/sns/web/v2/
// login/code with the params in the JSON body only (zone as the string "86",
// never in the URL query). postJSONOpts appends params to the query string
// too, which the login-guild answers with -101 "无登录信息".
func TestPhoneLoginWireMatchesReference(t *testing.T) {
	rec := &urlRecorder{}
	c := newXHSClient(clientOptions{})
	c.http = &http.Client{Transport: rec}
	c.cookies.set("a1", generateA1())
	c.cookies.set("webId", "w1")

	o := phoneSignOpts()
	o.body = `{"mobile_token":` + marshalJSONScalar("tokval") +
		`,"zone":` + marshalJSONScalar("86") + `,"phone":` + marshalJSONScalar("13800138000") + `}`
	if _, _, err := c.postJSONOpts(loginCodeURL, nil, o); err != nil {
		t.Fatalf("postJSONOpts: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.urls) != 1 {
		t.Fatalf("got %d requests, want 1", len(rec.urls))
	}
	if got := rec.urls[0].Path; got != "/api/sns/web/v2/login/code" {
		t.Errorf("login/code URL path = %q, want /api/sns/web/v2/login/code", got)
	}
	if q := rec.urls[0].RawQuery; q != "" {
		t.Errorf("login/code URL query = %q, want empty (params must be body-only)", q)
	}
	if got := rec.bodies[0]; got != `{"mobile_token":"tokval","zone":"86","phone":"13800138000"}` {
		t.Errorf("login/code body = %q, want ref-exact body with string zone", got)
	}
	if got := rec.cookies[0]; !strings.Contains(got, "a1=") {
		t.Errorf("login/code Cookie = %q, want a1 kept", got)
	}
}
