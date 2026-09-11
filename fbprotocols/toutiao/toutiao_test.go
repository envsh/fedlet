package toutiao

import (
	"testing"
)

// hotFixture mirrors two live rows of /hot-event/hot-board captured 2026-09-10.
const hotFixture = `{"status":"success","data":[
  {
    "ClusterId": 7682375308184387610,
    "ClusterIdStr": "7682375308184387610",
    "Title": "曝DeepSeek正筹备科创板IPO",
    "QueryWord": "曝DeepSeek正筹备科创板IPO",
    "Label": "",
    "LabelDesc": "",
    "InterestCategory": ["technology","finance"],
    "HotValue": "13830954",
    "Url": "https://www.toutiao.com/trending/7682375308184387610/?category_name=topic_innerflow&log_pb=%7B%22hot_board_cluster_id%22%3A%227682375308184387610%22%7D",
    "Image": {"url": "https://p3-sign.toutiaoimg.com/tos-cn-i-qvj2lq49k0/44c681ae879845d2867087d373693b2d~tplv-tt-shrink:960:540.jpeg?_iz=30575"}
  },
  {
    "ClusterId": 7682427521440721968,
    "ClusterIdStr": "7682427521440721968",
    "Title": "顺丰回应寄丢黄金",
    "QueryWord": "顺丰回应寄丢黄金",
    "Label": "", "LabelDesc": "",
    "InterestCategory": ["society"],
    "HotValue": 9327610,
    "Url": "https://www.toutiao.com/trending/7682427521440721968/?event_type=hot_board",
    "Image": {"url": "https://p3-sign.toutiaoimg.com/x~tplv"}
  }
]}`

// newsFixture mirrors the live /api/pc/feed rows captured 2026-09-10: one
// promoted ad, one video, one article.
const newsFixture = `{"message":"success","has_more":true,"next":{"max_behot_time":1789005764},"data":[
  {
    "title": "推广:健身卡3折限量",
    "source": "某广告主",
    "chinese_tag": "广告",
    "group_id": "9000000000000000001",
    "item_id": "9000000000000000001",
    "behot_time": 1789010000,
    "has_video": false,
    "comments_count": 0,
    "image_url": "//p1.pstatp.com/list/ad.jpg",
    "is_feed_ad": true
  },
  {
    "title": "小姑子结婚，丈夫仅收9块9",
    "source": "鑫悦追剧",
    "chinese_tag": "视频",
    "group_id": "7680431306274161175",
    "item_id": "7680431306274161175",
    "behot_time": 1789009814,
    "has_video": true,
    "comments_count": 790,
    "image_url": "//p3.pstatp.com/list/190x124/tos-cn-i-e874c6/ooEIiAfYEFm5gLAAQzkDEc2XL3vAeqVsZBSfEz",
    "is_feed_ad": false
  },
  {
    "title": "战斗升级！美国炸了5艘伊朗油轮",
    "source": "陈砚之",
    "chinese_tag": "国际",
    "group_id": "7683451425430503946",
    "item_id": "7683451425430503946",
    "behot_time": 1789009364,
    "has_video": false,
    "comments_count": 4210,
    "image_url": "//p1.pstatp.com/list/tos-cn-i-6w9my0ksvp/cbc76d9cc9744ae1af5f2916701d774c",
    "is_feed_ad": false
  }
]}`

func TestParseHotlist(t *testing.T) {
	r, err := parseHotlist([]byte(hotFixture))
	if err != nil {
		t.Fatalf("parseHotlist: %v", err)
	}
	if r.Status != "success" {
		t.Fatalf("status = %q", r.Status)
	}
	if len(r.Data) != 2 {
		t.Fatalf("len(Data) = %d, want 2", len(r.Data))
	}
	a, b := &r.Data[0], &r.Data[1]
	if a.Key() != "7682375308184387610" {
		t.Errorf("Key = %q", a.Key())
	}
	if got := HotlistTitle(a); got != "曝DeepSeek正筹备科创板IPO" {
		t.Errorf("Title = %q", got)
	}
	if got := HotlistHot(a); got != "1383.1w" {
		t.Errorf("Hot(string) = %q, want 1383.1w", got)
	}
	if got := HotlistHot(b); got != "932.8w" {
		t.Errorf("Hot(number) = %q, want 932.8w", got)
	}
	if got := HotlistDetail(a); got != "technology / finance / 热度 1383.1w" {
		t.Errorf("Detail = %q", got)
	}
	if got := HotlistLink(a); got != "https://www.toutiao.com/trending/7682375308184387610/" {
		t.Errorf("Link = %q", got)
	}
	if got := HotlistLink(b); got != "https://www.toutiao.com/trending/7682427521440721968/" {
		t.Errorf("Link(b) = %q", got)
	}
}

func TestParseNews(t *testing.T) {
	r, err := parseNews([]byte(newsFixture))
	if err != nil {
		t.Fatalf("parseNews: %v", err)
	}
	if r.Message != "success" {
		t.Fatalf("message = %q", r.Message)
	}
	if !r.HasMore || r.Next.MaxBehotTime != 1789005764 {
		t.Errorf("pagination = has_more %v, next %d", r.HasMore, r.Next.MaxBehotTime)
	}
	if len(r.Data) != 3 {
		t.Fatalf("len(Data) = %d, want 3", len(r.Data))
	}
	if !r.Data[0].IsAd() {
		t.Errorf("first item should be flagged ad")
	}
	v := &r.Data[1]
	if v.Key() != "7680431306274161175" {
		t.Errorf("Key = %q", v.Key())
	}
	if got := NewsLink(v); got != "https://www.toutiao.com/article/7680431306274161175/" {
		t.Errorf("Link = %q", got)
	}
	if !NewsVideo(v) {
		t.Errorf("video item should report HasVideo")
	}
	if got := NewsComments(v); got != "790" {
		t.Errorf("Comments = %q, want 790", got)
	}
	if got := NewsTag(&r.Data[2]); got != "国际" {
		t.Errorf("Tag = %q", got)
	}
}

// TestNewsWatermark exercises the newsRound publish gate against a fake
// publish callback: items at or below the watermark must be skipped.
func TestNewsWatermark(t *testing.T) {
	fixture := func() *NewsResp {
		r, err := parseNews([]byte(newsFixture))
		if err != nil {
			t.Fatalf("parseNews: %v", err)
		}
		return r
	}
	origFetch := fetchNewsFn
	fetchNewsFn = func(int64) (*NewsResp, error) { return fixture(), nil }
	defer func() { fetchNewsFn = origFetch }()

	state := newState()
	state.LastBehot = fixture().Data[0].BehotTime // watermark = newest item's time

	var published []string
	SetPublishInfo(func(v any) error {
		m := v.(map[string]any)
		published = append(published, m["id"].(string))
		return nil
	})
	defer SetPublishInfo(nil)

	newsRound(state)
	// Nothing is newer than the watermark, so nothing may publish.
	if len(published) != 0 {
		t.Errorf("published %v despite watermark, want none", published)
	}
	// With an older watermark, only strictly newer rows publish (the ad is
	// skipped and the row at the watermark is not republished).
	state2 := newState()
	state2.LastBehot = fixture().Data[2].BehotTime
	published = nil
	newsRound(state2)
	if len(published) != 1 || published[0] != "7680431306274161175" {
		t.Errorf("published %v, want [7680431306274161175]", published)
	}
	if state2.LastBehot != fixture().Data[0].BehotTime {
		t.Errorf("watermark advanced to %d, want %d", state2.LastBehot, fixture().Data[0].BehotTime)
	}
}

func TestStateRoundTrip(t *testing.T) {
	s := newState()
	s.Hotlist["a"] = 1
	s.News["b"] = 2
	s.LastBehot = 3
	path := t.TempDir() + "/toutiao-state.json"
	saveState(path, s)
	got := loadState(path)
	if got.Hotlist["a"] != 1 || got.News["b"] != 2 || got.LastBehot != 3 {
		t.Errorf("round trip mismatch: %+v", got)
	}
}

func TestHotValueNumberAndString(t *testing.T) {
	// HotlistHot must tolerate numeric display already covered by fixture rows.
	// Just confirm the AuthStatus/AuthUser contract stays public.
	if AuthStatus() != "public" {
		t.Errorf("AuthStatus = %q, want public", AuthStatus())
	}
	if AuthUser() != "toutiao" {
		t.Errorf("AuthUser = %q", AuthUser())
	}
}
