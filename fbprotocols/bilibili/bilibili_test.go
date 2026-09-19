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
		"major":{"archive":{"title":"标题A","bvid":"BV1x","aid":42,"desc":"d","cover":"http://cover/v.jpg"}}}}}`)
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
	if got := feedImage(&it); got != "http://cover/v.jpg" {
		t.Fatalf("feedImage(archive)=%q", got)
	}
	if got := feedImages(&it); got != nil {
		t.Fatalf("feedImages(archive)=%v, want nil", got)
	}

	var fp feedItem
	if err := json.Unmarshal([]byte(`{"id_str":"","type":"DYNAMIC_TYPE_DRAW",
		"modules":{"module_dynamic":{"major":{"draw":{"title":"绘画",
		"items":[{"src":"http://img/a.png"},{"src":"http://img/b.png"}]}}}}}`), &fp); err != nil {
		t.Fatal(err)
	}
	if got := feedText(&fp); got != "绘画" {
		t.Fatalf("draw feedText=%q", got)
	}
	if got := feedItemID(&fp); got != "feed:DYNAMIC_TYPE_DRAW:0" {
		t.Fatalf("fallback feedItemID=%q", got)
	}
	if got := feedImage(&fp); got != "http://img/a.png" {
		t.Fatalf("feedImage(draw)=%q", got)
	}
	if got := feedImages(&fp); len(got) != 2 || got[1] != "http://img/b.png" {
		t.Fatalf("feedImages(draw)=%v", got)
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
	if p["proto_type"] != "hotboard" || p["bvid"] != "BV2B" || p["title"] != "第二条" {
		t.Fatalf("bad payload: %+v", p)
	}
	if p["cycle_count"] != json.Number("2") {
		t.Fatalf("bad cycle_count: %+v", p)
	}
	if _, ok := p["url"]; ok {
		t.Fatalf("custom url key leaked: %+v", p)
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
{"id_str":"d1","type":"DYNAMIC_TYPE_AV","modules":{"module_author":{"name":"a","mid":1},"module_dynamic":{"desc":{"text":"v1"},"major":{"archive":{"title":"T1","bvid":"BVX1","cover":"http://cover/d1.jpg"}}}}},
{"id_str":"d2","type":"DYNAMIC_TYPE_DRAW","modules":{"module_author":{"name":"b","mid":2},"module_dynamic":{"desc":{"text":"v2"},"major":{"draw":{"title":"D2","items":[{"src":"http://img/1.png"},{"src":"http://img/2.png"}]}}}}}
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
	if p["proto_type"] != "follow_feed" || p["id_str"] != "d2" {
		t.Fatalf("bad payload: %+v", p)
	}
	if p["cycle_count"] != json.Number("2") {
		t.Fatalf("bad cycle_count: %+v", p)
	}
	ma, _ := p["modules"].(map[string]any)
	md, _ := ma["module_dynamic"].(map[string]any)
	major, _ := md["major"].(map[string]any)
	draw, _ := major["draw"].(map[string]any)
	if draw["title"] != "D2" {
		t.Fatalf("bad draw title: %+v", draw)
	}
	if ims, _ := draw["items"].([]any); len(ims) != 2 {
		t.Fatalf("bad draw items: %+v", draw)
	}
}

// ---- notify parsing ----

func TestNotifyParseReplyTolerant(t *testing.T) {
	resetBili(t)
	jar.set("SESSDATA", "sess")
	body := []byte(`{"code":0,"data":{"items":[
	{"id":1,"reply_time":1700000000,
	 "user":{"mid":7,"nickname":"甲"},
	 "item":{"title":"主题","message":"评论内容","root_reply_content":"","source_content":"","uri":"https://t.bilibili.com/1"}},
	{"id":2}
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
	// Entry without a user/item object must be tolerated (empty but present).
	if evs[1].ID != 2 || evs[1].Uname != "" {
		t.Fatalf("bare entry: %+v", evs[1])
	}
}

func TestNotifyParseLikeTolerant(t *testing.T) {
	resetBili(t)
	jar.set("SESSDATA", "sess")
	body := []byte(`{"code":0,"data":{"latest":{"items":[]},"total":{"items":[
	{"id":11,"like_time":1700000011,
	 "users":[{"mid":8,"nickname":"乙"}],
	 "item":{"title":"视频标题","uri":"https://www.bilibili.com/video/BV1x"}}
	]}}}`)
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
	if ev.ID != 11 || ev.Ctime != 1700000011 || ev.Message != "赞了你的内容" {
		t.Fatalf("like event meta: %+v", ev)
	}
}

func TestNotifyRoundPayloadFields(t *testing.T) {
	resetBili(t)
	jar.set("SESSDATA", "sess")

	replyRound, likeRound, sysRound := 0, 0, 0
	oldHC := hc
	hc = &http.Client{Transport: stubTransport(func(r *http.Request) (*http.Response, error) {
		var body []byte
		switch {
		case strings.HasPrefix(r.URL.String(), unreadURL):
			body = []byte(`{"code":0,"data":{"reply":3,"at":0,"like":0,"sys_msg":2}}`)
		case strings.HasPrefix(r.URL.String(), atURL):
			body = []byte(`{"code":0,"data":{"cursor":{"is_end":true},"items":[]}}`)
		case strings.HasPrefix(r.URL.String(), replyURL):
			replyRound++
			id := 100 + replyRound - 1
			body = []byte(fmt.Sprintf(`{"code":0,"data":{"items":[{"id":%d,"reply_time":1700000001,"user":{"mid":7,"nickname":"甲","avatar":"http://av/a.jpg","fans":3,"follow":false},"item":{"subject_id":55,"source_id":66,"target_id":77,"type":"reply","business":"评论","title":"回复主题","image":"http://img/r.jpg","root_reply_content":"","source_content":"回复内容","target_reply_content":"被回复内容","at_details":[{"mid":1,"nickname":"被@甲"}],"topic_details":[{"topic_id":9,"topic_content":"话题A"}],"uri":"https://t.bilibili.com/1"},"counts":1}]}}}`, id))
		case strings.HasPrefix(r.URL.String(), likeURL):
			likeRound++
			id := 200 + likeRound - 1
			body = []byte(fmt.Sprintf(`{"code":0,"data":{"latest":{"items":[]},"total":{"items":[{"id":%d,"like_time":1700000002,"users":[{"mid":8,"nickname":"乙","avatar":"http://av/b.jpg","fans":0,"follow":false}],"item":{"item_id":440,"type":"video","business":"视频","title":"视频标题","image":"http://img/l.jpg","desc":"-","uri":"https://www.bilibili.com/video/BV1x","ctime":1600000000},"counts":5}]}}}`, id))
		case strings.HasPrefix(r.URL.String(), sysMsgURL):
			sysRound++
			id := 300 + sysRound - 1
			body = []byte(fmt.Sprintf(`{"code":0,"data":{"system_notify_list":[{"id":%d,"type":4,"card_type":2,"title":"系统通知","content":"内容","card_brief":"brief","card_msg_brief":"msgBrief","card_story_title":"故事","card_cover":"http://img/card.jpg","card_link":"https://b23.tv/x","source":{"name":"源","logo":"http://img/logo.png"},"publisher":{"name":"官","mid":9,"face":"http://av/f.jpg"},"time_at":"2023-04-17 18:03:08"}]}}`, id))
		default:
			body = []byte(`{"code":0,"data":{}}`)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})}
	defer func() { hc = oldHC }()

	got := guardPublish(t)
	state := newBiliState()

	if n := notifyRound(state); n != 0 {
		t.Fatalf("seed round published %d detail events", n)
	}
	if len(*got) != 1 {
		t.Fatalf("seed round published %d payloads (%+v)", len(*got), *got)
	}
	un := (*got)[0]
	if un["kind"] != "notify_unread" || un["reply"] != int64(3) || un["sys_msg"] != int64(2) || un["total"] != int64(5) {
		t.Fatalf("bad unread payload: %+v", un)
	}

	*got = (*got)[:0]
	if n := notifyRound(state); n != 3 {
		t.Fatalf("second round published %d detail events, want 3", n)
	}
	var rp, lp, sp map[string]any
	for _, p := range *got {
		if p["proto_type"] != "notify_event" || p["cycle_count"] != json.Number("1") {
			t.Fatalf("bad proto fields: %+v", p)
		}
		switch {
		case p["reply_time"] != nil:
			rp = p
		case p["like_time"] != nil:
			lp = p
		case p["card_brief"] != nil:
			sp = p
		default:
			t.Fatalf("unknown event payload: %+v", p)
		}
	}
	if rp == nil || lp == nil || sp == nil {
		t.Fatalf("missing event kinds: %+v", *got)
	}

	if rp["id"] != json.Number("101") {
		t.Fatalf("bad reply id: %+v", rp)
	}
	ru, _ := rp["user"].(map[string]any)
	if ru == nil || ru["nickname"] != "甲" || ru["mid"] != json.Number("7") || ru["avatar"] != "http://av/a.jpg" {
		t.Fatalf("bad reply user: %+v", rp["user"])
	}
	ri, _ := rp["item"].(map[string]any)
	if ri == nil || ri["subject_id"] != json.Number("55") || ri["source_id"] != json.Number("66") || ri["target_id"] != json.Number("77") {
		t.Fatalf("bad reply item ids: %+v", rp["item"])
	}
	if ri["type"] != "reply" || ri["business"] != "评论" || ri["image"] != "http://img/r.jpg" {
		t.Fatalf("bad reply item meta: %+v", rp["item"])
	}
	if ri["target_reply_content"] != "被回复内容" {
		t.Fatalf("bad reply target content: %+v", rp["item"])
	}
	if ad, _ := ri["at_details"].([]any); len(ad) != 1 {
		t.Fatalf("bad reply at_details: %+v", rp["item"])
	}
	if td, _ := ri["topic_details"].([]any); len(td) != 1 {
		t.Fatalf("bad reply topic_details: %+v", rp["item"])
	}
	if rp["counts"] != json.Number("1") {
		t.Fatalf("bad reply counts: %+v", rp)
	}

	if lp["id"] != json.Number("201") {
		t.Fatalf("bad like id: %+v", lp)
	}
	li, _ := lp["item"].(map[string]any)
	if li == nil || li["item_id"] != json.Number("440") || li["type"] != "video" || li["business"] != "视频" {
		t.Fatalf("bad like item: %+v", lp["item"])
	}
	if li["image"] != "http://img/l.jpg" || li["ctime"] != json.Number("1600000000") {
		t.Fatalf("bad like item meta: %+v", lp["item"])
	}
	if lp["counts"] != json.Number("5") {
		t.Fatalf("bad like counts: %+v", lp)
	}

	if sp["id"] != json.Number("301") || sp["type"] != json.Number("4") || sp["card_type"] != json.Number("2") {
		t.Fatalf("bad sys types: %+v", sp)
	}
	if sp["card_brief"] != "brief" || sp["card_msg_brief"] != "msgBrief" || sp["card_story_title"] != "故事" {
		t.Fatalf("bad sys card text: %+v", sp)
	}
	if sp["card_cover"] != "http://img/card.jpg" || sp["card_link"] != "https://b23.tv/x" {
		t.Fatalf("bad sys card: %+v", sp)
	}
	if spu, _ := sp["publisher"].(map[string]any); spu == nil || spu["name"] != "官" || spu["mid"] != json.Number("9") || spu["face"] != "http://av/f.jpg" {
		t.Fatalf("bad sys publisher: %+v", sp["publisher"])
	}
	if src, _ := sp["source"].(map[string]any); src == nil || src["name"] != "源" || src["logo"] != "http://img/logo.png" {
		t.Fatalf("bad sys source: %+v", sp["source"])
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
