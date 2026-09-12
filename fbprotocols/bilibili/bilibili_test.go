package bilibili

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

// ---- WBI signing ----

func TestKeyFromURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://i0.hdslb.com/bfs/wbi/6d6a9cfc9d6d9b8715a708d30e026390.png", "6d6a9cfc9d6d9b8715a708d30e026390"},
		{"https://i0.hdslb.com/bfs/wbi/xyzabc1234567890abcdef1234567890.png?x=1", "xyzabc1234567890abcdef1234567890"},
		{"https://i0.hdslb.com/bfs/wbi/nokey", "nokey"},
		{"", ""},
	}
	for _, c := range cases {
		if got := keyFromURL(c.in); got != c.want {
			t.Fatalf("keyFromURL(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}

func TestMixinFromKeys(t *testing.T) {
	// Deterministic and exactly 32 chars (the reorder table prefix).
	mk := wbiMixinFromKeys(
		"7cd084941338484aae1ad9425b84077c",
		"4932caff0ff746eab6f01bf08b70ac45")
	if len(mk) != 32 {
		t.Fatalf("mixin key length = %d, want 32", len(mk))
	}
	if mk != wbiMixinFromKeys(
		"7cd084941338484aae1ad9425b84077c",
		"4932caff0ff746eab6f01bf08b70ac45") {
		t.Fatal("mixin key not deterministic")
	}
	if mk == wbiMixinFromKeys(
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb") {
		t.Fatal("mixin key must depend on the input keys")
	}
}

func TestURiEncodeCaps(t *testing.T) {
	if got := uriEncodeCaps("a~b C"); got != "a~b%20C" {
		t.Fatalf("uriEncodeCaps = %q", got)
	}
	if got := uriEncodeCaps(`关键`); !strings.Contains(strings.ToUpper(got), "%") {
		t.Fatalf("non-ascii must be percent encoded upper-case, got %q", got)
	}
	for _, r := range uriEncodeCaps(`a b%c/` + "关键") {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.~%0123456789ABCDEF", r) {
			t.Fatalf("unexpected char %q in %q", r, uriEncodeCaps(`a b%c/`))
			break
		}
	}
}

func TestWBISignFormat(t *testing.T) {
	wbiMu.Lock()
	wbiKeysData = wbiKeys{mixinKey: "01234567890123456789012345678901"}
	wbiMu.Unlock()

	q, err := wbiSign(map[string]string{"ps": "30", "fresh_type": "4"})
	if err != nil {
		t.Fatalf("wbiSign: %v", err)
	}
	for _, want := range []string{"wts=", "w_rid=", "ps=30", "fresh_type=4"} {
		if !strings.Contains(q, want) {
			t.Fatalf("wbiSign query %q missing %q", q, want)
		}
	}
	if !wbiReady() {
		t.Fatal("wbiReady must be true after seeding the mixin key")
	}
	wbiMu.Lock()
	wbiKeysData = wbiKeys{}
	wbiMu.Unlock()

	if _, err := wbiSign(map[string]string{}); err == nil {
		t.Fatal("wbiSign without mixin key must error")
	}
}

// ---- error classification ----

func TestClassifyAPIError(t *testing.T) {
	cases := []struct {
		code int
		want error
	}{
		{-101, ErrNotLoggedIn},
		{-401, ErrNotLoggedIn},
		{-352, ErrRiskControl},
		{-412, ErrRiskControl},
		{-403, ErrRiskControl},
		{-400, nil},
		{-100, nil},
		{-799, nil},
	}
	for _, c := range cases {
		got := classifyAPIError("https://x", c.code, "")
		if err := errors.Is(got, c.want); (c.want == nil) == err && c.want == nil {
			continue
		}
		if c.want != nil && !errors.Is(got, c.want) {
			t.Fatalf("code %d err=%v, want %v", c.code, got, c.want)
		}
		if c.want == nil && errors.Is(got, ErrNotLoggedIn) {
			t.Fatalf("code %d must be a plain error, got %v", c.code, got)
		}
	}
}

// ---- id / text helpers ----

func TestFeedTextAndURL(t *testing.T) {
	var it feedItem
	body := []byte(`{"id_str":"12345","type":"DYNAMIC_TYPE_AV",
		"modules":{"module_author":{"name":"up1","mid":99},
		"module_dynamic":{"desc":{"text":"发布了一个视频"},
		"major":{"archive":{"title":"标题A","bvid":"BV1x","aid":42,"desc":"d"}}}}}`)
	if err := json.Unmarshal(body, &it); err != nil {
		t.Fatal(err)
	}
	if got := feedText(&it); got != "标题A" {
		t.Fatalf("feedText=%q", got)
	}
	if got := feedURLFor(&it); got != "https://www.bilibili.com/video/BV1x" {
		t.Fatalf("feedURLFor=%q", got)
	}
	if got := feedItemID(&it); got != "12345" {
		t.Fatalf("feedItemID=%q", got)
	}

	var fp feedItem
	if err := json.Unmarshal([]byte(`{"id_str":"","type":"DYNAMIC_TYPE_DRAW",
		"modules":{"module_dynamic":{"major":{"draw":{"title":"绘画"}}}}}`), &fp); err != nil {
		t.Fatal(err)
	}
	if got := feedText(&fp); got != "绘画" {
		t.Fatalf("draw feedText=%q", got)
	}
	if got := feedItemID(&fp); got != "feed:DYNAMIC_TYPE_DRAW:0" {
		t.Fatalf("fallback feedItemID=%q", got)
	}
}

func TestHotItemID(t *testing.T) {
	if got := hotItemID(&rankingItem{Bvid: "BV1x"}); got != "BV1x" {
		t.Fatalf("bvid id=%q", got)
	}
	if got := hotItemID(&rankingItem{Aid: 42}); got != "av42" {
		t.Fatalf("aid id=%q", got)
	}
}

func TestNumAnyAsInt(t *testing.T) {
	cases := []struct {
		v    any
		want int64
		ok   bool
	}{
		{json.Number("123"), 123, true},
		{float64(7), 7, true},
		{int64(8), 8, true},
		{"9", 9, true},
		{"abc", 0, false},
		{nil, 0, false},
	}
	for _, c := range cases {
		got, ok := numAnyAsInt(c.v)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("numAnyAsInt(%v)=%d/%v, want %d/%v", c.v, got, ok, c.want, c.ok)
		}
	}
}

// ---- stub HTTP client ----

type stubTransport func(*http.Request) (*http.Response, error)

func (f stubTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubClient returns "{}" for every URL except target.
func stubClient(target string, targetBody []byte, status int, fallback []byte) *http.Client {
	return &http.Client{Transport: stubTransport(func(r *http.Request) (*http.Response, error) {
		body, code := fallback, http.StatusOK
		if r.URL.String() == target {
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

// resetBili resets the mutable package state that tests mutate.
func resetBili(t *testing.T) {
	t.Helper()
	jar = newCookieStore()
	wbiMu.Lock()
	wbiKeysData = wbiKeys{}
	wbiMu.Unlock()
	auth.mu.Lock()
	auth.status = AuthStatusEmpty
	auth.user = ""
	auth.loginMethod = ""
	auth.refreshToken = ""
	auth.mu.Unlock()
	authMu.Lock()
	lastAuthErr = nil
	reLogin.nextAt = time.Time{}
	refreshInFlight = false
	rateLimit.backoffUntil = time.Time{}
	authMu.Unlock()
	notifyLastUnread = nil
}

func guardPublish(t *testing.T) *[]map[string]any {
	t.Helper()
	old := publish
	got := &[]map[string]any{}
	publish = func(p map[string]any) error {
		*got = append(*got, p)
		return nil
	}
	t.Cleanup(func() { publish = old })
	return got
}

// ---- round tests (hotboard) ----

const hotTestBody = `{"code":0,"data":{"list":[
{"aid":1,"bvid":"BV1A","rank":1,"title":"第一条","owner":{"name":"up1"},"stat":{"view":100}},
{"aid":2,"bvid":"BV2B","rank":2,"title":"第二条","owner":{"name":"up2"}}
]}}`

func TestHotRoundFirstSyncPublishesCurrentList(t *testing.T) {
	resetBili(t)
	oldHC := hc
	hc = stubClient(rankingURL, []byte(hotTestBody), http.StatusOK, []byte("{}"))
	defer func() { hc = oldHC }()

	got := guardPublish(t)
	state := newBiliState()
	n := hotRound(state)

	// A public hot board publishes the whole current list on first poll, then
	// only whichever entries are new in later polls.
	if n != 2 {
		t.Fatalf("first sync published %d, want 2", n)
	}
	if len(*got) != 2 {
		t.Fatalf("first sync published %d items", len(*got))
	}
	if len(state.Hotboard) != 2 || state.Hotboard["BV1A"] == 0 {
		t.Fatalf("first sync did not seed both ids: %+v", state.Hotboard)
	}
	// Second poll: nothing new.
	*got = (*got)[:0]
	hotRound(state)
	if len(*got) != 0 {
		t.Fatalf("re-published after dedupe: %+v", *got)
	}
}

func TestHotRoundPublishesNewOnly(t *testing.T) {
	resetBili(t)
	oldHC := hc
	hc = stubClient(rankingURL, []byte(hotTestBody), http.StatusOK, []byte("{}"))
	defer func() { hc = oldHC }()

	got := guardPublish(t)
	state := newBiliState()
	state.Hotboard["BV1A"] = time.Now().Unix()

	n := hotRound(state)
	if n != 1 {
		t.Fatalf("published %d, want 1", n)
	}
	if len(*got) != 1 {
		t.Fatalf("payload count=%d", len(*got))
	}
	p := (*got)[0]
	if p["kind"] != "hotboard" || p["bvid"] != "BV2B" || p["title"] != "第二条" {
		t.Fatalf("bad payload: %+v", p)
	}
	if p["url"] != "https://www.bilibili.com/video/BV2B" {
		t.Fatalf("bad url: %+v", p)
	}
	// second round: everything known, nothing published.
	*got = (*got)[:0]
	hotRound(state)
	if len(*got) != 0 {
		t.Fatalf("re-published after dedupe: %+v", *got)
	}
}

func TestHotRoundSkipsOnError(t *testing.T) {
	resetBili(t)
	oldHC := hc
	hc = stubClient(rankingURL, []byte("<html>block</html>"), http.StatusOK, []byte("{}"))
	defer func() { hc = oldHC }()
	got := guardPublish(t)
	hotRound(newBiliState())
	if len(*got) != 0 {
		t.Fatalf("HTML 200 shed must not publish, got %d", len(*got))
	}
}

// ---- round tests (follow feed) ----

const feedTestBody = `{"code":0,"data":{"items":[
{"id_str":"d1","type":"DYNAMIC_TYPE_AV","modules":{"module_author":{"name":"a","mid":1},"module_dynamic":{"desc":{"text":"v1"},"major":{"archive":{"title":"T1","bvid":"BVX1"}}}}},
{"id_str":"d2","type":"DYNAMIC_TYPE_DRAW","modules":{"module_author":{"name":"b","mid":2},"module_dynamic":{"desc":{"text":"v2"},"major":{"draw":{"title":"D2"}}}}}
]}}`

func TestFeedRoundRequiresSession(t *testing.T) {
	resetBili(t)
	oldHC := hc
	hc = stubClient(feedURL, []byte(feedTestBody), http.StatusOK, []byte("{}"))
	defer func() { hc = oldHC }()

	got := guardPublish(t)
	feedRound(newBiliState())
	if len(*got) != 0 {
		t.Fatalf("feed without session published %d items", len(*got))
	}
}

func TestFeedRoundFirstSyncSeedsNoPublish(t *testing.T) {
	resetBili(t)
	jar.set("SESSDATA", "sess")
	oldHC := hc
	hc = stubClient(feedURL, []byte(feedTestBody), http.StatusOK, []byte("{}"))
	defer func() { hc = oldHC }()

	got := guardPublish(t)
	state := newBiliState()
	if fed := feedRound(state); fed != 0 {
		t.Fatalf("first sync published %d", fed)
	}
	if len(*got) != 0 || len(state.FollowFeed) != 2 || state.FollowFeed["d1"] == 0 {
		t.Fatalf("seed wrong: publishes=%d state=%+v", len(*got), state.FollowFeed)
	}
}

func TestFeedRoundPublishesNewOnly(t *testing.T) {
	resetBili(t)
	jar.set("SESSDATA", "sess")
	oldHC := hc
	hc = stubClient(feedURL, []byte(feedTestBody), http.StatusOK, []byte("{}"))
	defer func() { hc = oldHC }()

	got := guardPublish(t)
	state := newBiliState()
	state.FollowFeed["d1"] = time.Now().Unix()

	n := feedRound(state)
	if n != 1 {
		t.Fatalf("published %d, want 1", n)
	}
	p := (*got)[0]
	if p["kind"] != "follow_feed" || p["id"] != "d2" || p["author"] != "b" {
		t.Fatalf("bad payload: %+v", p)
	}
	if p["text"] != "D2" {
		t.Fatalf("bad draw text: %+v", p)
	}
}

// ---- notify parsing ----

func TestNotifyParseReplyTolerant(t *testing.T) {
	resetBili(t)
	jar.set("SESSDATA", "sess")
	body := []byte(`{"code":0,"data":{"items":[
	{"id":1,"ctime":1700000000,"dynamic_url":"https://t.bilibili.com/1",
	 "reply":{"content":{"message":"评论内容"},"member":{"uname":"甲","mid":7}}},
	{"id":2,"ctime":1700000001}
	]}}`)
	oldHC := hc
	hc = stubClient(replyURL, body, http.StatusOK, []byte(`{"code":0,"data":{"items":[]}}`))
	defer func() { hc = oldHC }()

	evs, err := fetchReplyEvents()
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("events=%d, want 2 (%+v)", len(evs), evs)
	}
	if evs[0].Type != "reply" || evs[0].Uname != "甲" || evs[0].Message != "评论内容" || evs[0].Mid != 7 {
		t.Fatalf("reply event: %+v", evs[0])
	}
	if evs[0].DynamicURL != "https://t.bilibili.com/1" || evs[0].Ctime != 1700000000 {
		t.Fatalf("reply event meta: %+v", evs[0])
	}
	// Entry without a reply object must be tolerated (empty but present).
	if evs[1].ID != 2 || evs[1].Uname != "" {
		t.Fatalf("bare entry: %+v", evs[1])
	}
}

func TestNotifyParseLikeTolerant(t *testing.T) {
	resetBili(t)
	jar.set("SESSDATA", "sess")
	body := []byte(`{"code":0,"data":{"items":[
	{"id":11,"ctime":1700000010,"like_time":1700000011,
	 "user":{"uname":"乙","mid":8},"video_title":"视频标题"}
	]}}`)
	oldHC := hc
	hc = stubClient(likeURL, body, http.StatusOK, []byte(`{"code":0,"data":{"items":[]}}`))
	defer func() { hc = oldHC }()

	evs, err := fetchLikeEvents()
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("events=%d, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Type != "like" || ev.Uname != "乙" || ev.Mid != 8 || ev.Subject != "视频标题" {
		t.Fatalf("like event: %+v", ev)
	}
	if ev.ID != 11 || ev.Ctime != 1700000010 {
		t.Fatalf("like event meta: %+v", ev)
	}
}

// ---- state ----

func TestStatePrune(t *testing.T) {
	s := newBiliState()
	now := time.Now()
	s.Hotboard["old"] = now.Add(-(dedupeExpiry + time.Hour)).Unix()
	s.Hotboard["fresh"] = now.Unix()
	pruneMap(s.Hotboard)
	if _, ok := s.Hotboard["old"]; ok {
		t.Fatal("stale hotboard id survived prune")
	}
	if _, ok := s.Hotboard["fresh"]; !ok {
		t.Fatal("fresh hotboard id pruned")
	}
}

func TestStateLoadNilSafe(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bilibili-state.json")
	if err := os.WriteFile(p, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	st := loadState()
	if st.Hotboard == nil || st.FollowFeed == nil || st.Notifications == nil {
		t.Fatal("nil maps after loading an empty state file")
	}
}

// ---- auth persistence ----

func TestAuthPersistenceLoginMethod(t *testing.T) {
	resetBili(t)
	t.Setenv("HOME", t.TempDir())

	jar.set("SESSDATA", "sess-test")
	jar.set("bili_jct", "jct-test")
	auth.mu.Lock()
	auth.status = AuthStatusReady
	auth.user = "up主"
	auth.loginMethod = "qr"
	auth.refreshToken = "tok"
	auth.mu.Unlock()
	saveAuth()

	// Wipe runtime + jar, reload from disk.
	resetBili(t)
	loadAuth()

	if got := jar.get("SESSDATA"); got != "sess-test" {
		t.Fatalf("SESSDATA not restored: %q", got)
	}
	if got := jar.get("bili_jct"); got != "jct-test" {
		t.Fatalf("bili_jct not restored: %q", got)
	}
	auth.mu.Lock()
	defer auth.mu.Unlock()
	if auth.user != "up主" || auth.loginMethod != "qr" || auth.refreshToken != "tok" {
		t.Fatalf("auth not restored: %+v", auth)
	}
	if auth.status != AuthStatusReady {
		t.Fatalf("status=%q", auth.status)
	}
}

func TestAuthInitialEmpty(t *testing.T) {
	resetBili(t)
	if got := AuthStatus(); got != AuthStatusEmpty {
		t.Fatalf("initial AuthStatus=%q, want empty", got)
	}
}

func TestAuthFileMissingLoadIsEmpty(t *testing.T) {
	resetBili(t)
	t.Setenv("HOME", t.TempDir())
	loadAuth()
	if got := AuthStatus(); got != AuthStatusEmpty {
		t.Fatalf("missing auth file should load empty, got %q", got)
	}
}

// ---- login gateway ----

func resetGateway() {
	authMu.Lock()
	reLogin.nextAt = time.Time{}
	lastAuthErr = nil
	rateLimit.backoffUntil = time.Time{}
	authMu.Unlock()
}

func TestEnsureSessionReadyNoUI(t *testing.T) {
	resetBili(t)
	resetGateway()
	t.Setenv("HOME", t.TempDir())

	origProbe, origUI := probeSession, startLoginUIFn
	defer func() { probeSession, startLoginUIFn = origProbe, origUI }()

	uiSpy := 0
	probeSession = func() error { return nil }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	auth.mu.Lock()
	auth.status = AuthStatusReady
	auth.mu.Unlock()

	if err := ensureSession(); err != nil {
		t.Fatalf("ready session errored: %v", err)
	}
	if uiSpy != 0 {
		t.Fatalf("ready session popped the login UI %d times", uiSpy)
	}
}

func TestEnsureSessionEmptyOpensLogin(t *testing.T) {
	resetBili(t)
	resetGateway()
	t.Setenv("HOME", t.TempDir())

	origProbe, origUI := probeSession, startLoginUIFn
	defer func() { probeSession, startLoginUIFn = origProbe, origUI }()

	uiSpy := 0
	probeSession = func() error { return nil }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	err := ensureSession()
	var pending *errLoginPending
	if !errors.As(err, &pending) {
		t.Fatalf("empty session should open the login UI and report pending, got %v", err)
	}
	if uiSpy != 1 {
		t.Fatalf("UI opened %d times, want 1", uiSpy)
	}
}

func TestEnsureSessionInvalidRefreshPath(t *testing.T) {
	resetBili(t)
	resetGateway()
	t.Setenv("HOME", t.TempDir())

	origProbe, origRefresh, origUI := probeSession, refreshCookies, startLoginUIFn
	defer func() { probeSession, refreshCookies, startLoginUIFn = origProbe, origRefresh, origUI }()

	uiSpy := 0
	probeSession = func() error { return ErrNotLoggedIn }
	refreshCookies = func() error { return ErrNotLoggedIn }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	auth.mu.Lock()
	auth.status = AuthStatusInvalid
	auth.mu.Unlock()

	err := ensureSession()
	var pending *errLoginPending
	if !errors.As(err, &pending) {
		t.Fatalf("invalid session after failed refresh should open login UI, got %v", err)
	}
	if uiSpy != 1 {
		t.Fatalf("UI opened %d times, want 1", uiSpy)
	}
}

func TestEnsureSessionCooldownSkipsUI(t *testing.T) {
	resetBili(t)
	resetGateway()
	t.Setenv("HOME", t.TempDir())

	origProbe, origUI := probeSession, startLoginUIFn
	defer func() { probeSession, startLoginUIFn = origProbe, origUI }()

	uiSpy := 0
	probeSession = func() error { return nil }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	armReLoginCooldown()
	err := ensureSession()
	if err == nil || !strings.Contains(err.Error(), "冷却") {
		t.Fatalf("cooldown should return a pause error, got %v", err)
	}
	if uiSpy != 0 {
		t.Fatalf("cooldown still opened the UI %d times", uiSpy)
	}
}

// ---- recommend (pull-only) ----

func TestFetchRecommendNoSessionFailsFast(t *testing.T) {
	resetBili(t)
	calls := 0
	oldHC := hc
	hc = &http.Client{Transport: stubTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
	})}
	defer func() { hc = oldHC }()

	if _, err := FetchRecommend(5); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("want ErrNotLoggedIn, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("made %d network calls without a session", calls)
	}
}

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
			t.Fatalf("uiAddrUsable(%q)=%v, want %v", c.addr, got, c.ok)
		}
	}
}

func TestRunAuthRoundGates(t *testing.T) {
	resetBili(t)
	resetGateway()
	t.Setenv("HOME", t.TempDir())

	origProbe, origUI := probeSession, startLoginUIFn
	defer func() { probeSession, startLoginUIFn = origProbe, origUI }()

	uiSpy := 0
	probeSession = func() error { return nil }
	startLoginUIFn = func() (string, error) { uiSpy++; return "http://127.0.0.1:0/", nil }

	ran := 0
	auth.mu.Lock()
	auth.status = AuthStatusEmpty
	auth.mu.Unlock()
	runAuthRound(func(s *biliState) int { ran++; return 0 }, newBiliState())
	if ran != 0 {
		t.Fatalf("round ran behind an empty session")
	}
	if uiSpy != 1 {
		t.Fatalf("gate should have opened the login UI once, got %d", uiSpy)
	}
}

func TestLookComponentNames(t *testing.T) {
	// The login page must expose the cookie fields the UI sends.
	for _, want := range []string{"SESSDATA", "bili_jct", "DedeUserID", "refresh_token", "ac_time_value"} {
		if !strings.Contains(loginPageHTML, want) {
			t.Fatalf("login page missing %q", want)
		}
	}
	if !strings.Contains(loginPageHTML, "/api/cookie") {
		t.Fatal("login page must call /api/cookie")
	}
}

func TestIsWBIStale(t *testing.T) {
	if !isWBIStale(fmt.Errorf("bilibili: code -403: w_rid 校验")) {
		t.Fatal("-403/w_rid text must be flagged stale")
	}
	if isWBIStale(errors.New("net down")) {
		t.Fatal("unrelated error must not be flagged stale")
	}
}
