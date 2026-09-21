package weibo

import (
	"encoding/json"
	"sync"
	"testing"
)

func TestParseHotlistRealtime(t *testing.T) {
	body := `{"ok":1,"data":{"realtime":[
		{"word":"警方调查南方医科大学跳楼","word_scheme":"#警方调查南方医科大学跳楼#","note":"警方调查南方医科大学跳楼","num":6860960,"rank":0,"realpos":1,"label_name":"","icon_desc":"新"},
		{"word":"男子30年前存一万定期忘取","word_scheme":"#男子30年前存一万定期忘取#","note":"","num":"1544992","rank":1,"realpos":2,"label_name":"沸","icon_desc":"沸"}
	]}}`
	var r WeiboResp
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.OK != 1 || len(r.Data.Realtime) != 2 {
		t.Fatalf("bad envelope: %+v", r)
	}
	a := r.Data.Realtime[0]
	if WeiboTitle(&a) != "警方调查南方医科大学跳楼" || WeiboLabel(&a) != "新" {
		t.Fatalf("item0 title/label: %q / %q", WeiboTitle(&a), WeiboLabel(&a))
	}
	if got := WeiboHot(&a); got != "686.1w" {
		t.Fatalf("item0 heat: got %q", got)
	}
	b := r.Data.Realtime[1]
	if got := WeiboHot(&b); got != "154.5w" {
		t.Fatalf("item1 string heat: got %q", got)
	}
	if WeiboLabel(&b) != "沸" {
		t.Fatalf("item1 label: got %q", WeiboLabel(&b))
	}
	if got := WeiboLink(b.Word); got != "https://s.weibo.com/weibo?q=%23%E7%94%B7%E5%AD%9030%E5%B9%B4%E5%89%8D%E5%AD%98%E4%B8%80%E4%B8%87%E5%AE%9A%E6%9C%9F%E5%BF%98%E5%8F%96%23&Refer=top" {
		t.Fatalf("item1 link: got %q", got)
	}
}

func TestParseHotlistGov(t *testing.T) {
	body := `{"ok":1,"data":{"hotgov":{"word":"#习近平致信祝贺吉林大学建校80周年#","note":"n","pos":1,"icon_desc":"热","is_hot":true},"hotgovs":[{"word":"#中国代表团巴黎奥运再创纪录#","pos":2,"icon_desc":"热"}]}}`
	var r WeiboResp
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	g := r.Data.Hotgov
	if g.Word != "#习近平致信祝贺吉林大学建校80周年#" || len(g.IsHot) == 0 {
		t.Fatalf("bad gov: %+v", g)
	}
	if len(r.Data.Hotgovs) != 1 {
		t.Fatalf("bad hotgovs: %+v", r.Data.Hotgovs)
	}
	if got := WeiboTitle(&r.Data.Hotgovs[0]); got != "中国代表团巴黎奥运再创纪录" {
		t.Fatalf("hotgovs[0] title: got %q", got)
	}
}

func TestParseHotlistBandOptional(t *testing.T) {
	body := `{"ok":1,"data":{"realtime":[{"word":"w"}],"band_list":[{"word":"某文娱话题","realpos":9}]}}`
	var r WeiboResp
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(r.Data.BandList) != 1 || r.Data.BandList[0].Realpos != 9 {
		t.Fatalf("bad band: %+v", r.Data.BandList)
	}
	body2 := `{"ok":1,"data":{"realtime":[{"word":"w"}]}}`
	var r2 WeiboResp
	if err := json.Unmarshal([]byte(body2), &r2); err != nil {
		t.Fatalf("unmarshal2: %v", err)
	}
	if len(r2.Data.BandList) != 0 {
		t.Fatalf("band should be empty when absent")
	}
}

func TestParseHotlistBadOK(t *testing.T) {
	if _, err := parseHotlist([]byte(`{"ok":0,"data":null}`)); err == nil {
		t.Fatalf("expected error for ok!=1")
	}
	if _, err := parseHotlist([]byte(`not json`)); err == nil {
		t.Fatalf("expected error for bad json")
	}
}

func TestHotEntryKeyAndLink(t *testing.T) {
	it := &HotItem{Word: "#话题#"}
	if it.Key() != "#话题#" {
		t.Fatalf("key: got %q", it.Key())
	}
	if got := WeiboLink(it.Word); got != "https://s.weibo.com/weibo?q=%23%E8%AF%9D%E9%A2%98%23&Refer=top" {
		t.Fatalf("link: got %q", got)
	}
	if got := WeiboLink(""); got != "" {
		t.Fatalf("empty link: got %q", got)
	}
}

// testRoundResp builds a canned response (parsed from JSON so Raw is set)
// covering every board.
func testRoundResp() *WeiboResp {
	body := `{"ok":1,"data":{
		"realtime":[
			{"word":"热点甲","note":"n1","realpos":1,"num":6860960,"label_name":"新"},
			{"word":"热点乙","note":"n2","realpos":2,"num":"1544992","icon_desc":"沸"}
		],
		"hotgov":{"word":"#政务话题#","note":"ng","pos":1,"icon_desc":"热"},
		"hotgovs":[
			{"word":"#政务话题#","pos":1,"icon_desc":"热"},
			{"word":"#另一政务#","pos":2,"icon_desc":"热"}
		],
		"band_list":[{"word":"文娱话题","realpos":3}]
	}}`
	r, err := parseHotlist([]byte(body))
	if err != nil {
		panic(err)
	}
	return r
}

func TestHotRoundPublishesAllSections(t *testing.T) {
	sc := &syncCapture{}
	pubfn_ = sc.capture
	defer func() { pubfn_ = nil }()
	old := fetchHotlistFn
	fetchHotlistFn = func() (*WeiboResp, error) { return testRoundResp(), nil }
	defer func() { fetchHotlistFn = old }()

	state := &weiboState{Hot: map[string]int64{}}
	hotRound(state)

	if sc.n() != 5 {
		t.Fatalf("published %d, want 5 (2 realtime + 1 gov + 2 hotgovs + 1 band deduped to 1)", sc.n())
	}
	words := map[string]int{}
	for _, p := range sc.payloads() {
		if p["proto_type"] != "weibo_hot" {
			t.Fatalf("bad proto_type %v", p["proto_type"])
		}
		if p["cycle_count"] != float64(6) {
			t.Fatalf("bad cycle_count %v", p["cycle_count"])
		}
		if w, ok := p["word"].(string); ok {
			if u, _ := p["url"].(string); u != WeiboLink(w) {
				t.Fatalf("bad url for %q: %q", w, u)
			}
			words[w]++
		}
	}
	if len(words) != 5 {
		t.Fatalf("published words %d, want 5: %+v", len(words), words)
	}
	if words["#政务话题#"] != 1 {
		t.Fatalf("gov single+list should dedupe by section:word, got words %+v", words)
	}
	if len(state.Hot) != 5 {
		t.Fatalf("state set size %d, want 5", len(state.Hot))
	}
	if state.Hot["realtime:热点甲"] == 0 {
		t.Fatalf("state missing realtime key")
	}

	hotRound(state)
	if sc.n() != 5 {
		t.Fatalf("second round should publish nothing, got %d", sc.n())
	}
}

func TestHotRoundError(t *testing.T) {
	old := fetchHotlistFn
	fetchHotlistFn = func() (*WeiboResp, error) {
		return nil, errTest
	}
	defer func() { fetchHotlistFn = old }()
	state := &weiboState{Hot: map[string]int64{}}
	hotRound(state)
	if len(LastErrs()) == 0 {
		t.Fatalf("expected LastErrs populated on fetch failure")
	}
}

type syncCapture struct {
	mu sync.Mutex
	ps [][]byte
}

func (sc *syncCapture) capture(v any) error {
	b := v.(json.RawMessage)
	sc.mu.Lock()
	sc.ps = append(sc.ps, append(json.RawMessage(nil), b...))
	sc.mu.Unlock()
	return nil
}

func (sc *syncCapture) n() int {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return len(sc.ps)
}

func (sc *syncCapture) payloads() []map[string]any {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	out := make([]map[string]any, 0, len(sc.ps))
	for _, b := range sc.ps {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err == nil {
			out = append(out, m)
		}
	}
	return out
}

var errTest = &testErr{}

type testErr struct{}

func (e *testErr) Error() string { return "test error" }
