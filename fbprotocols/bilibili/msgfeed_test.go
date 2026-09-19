package bilibili

import (
	"encoding/json"
	"testing"
	"time"
)

const replyFeedFixture = `{"code":0,"message":"OK","ttl":1,"data":{"cursor":{"is_end":true,"id":823260581625886,"time":1749474709},"items":[{"id":823260581625886,"user":{"mid":3546910497441845,"fans":0,"nickname":"佘总累了","avatar":"https://i2.hdslb.com/bfs/face/e45c62bd47729e07dd01a788988be865ed3d210e.jpg","mid_link":"","follow":false},"item":{"subject_id":1073543151725051921,"root_id":0,"source_id":265141324256,"target_id":0,"type":"dynamic","business_id":17,"business":"动态","title":"我已成为哔哩哔哩第245743680位转正会员","desc":"","image":"http://i0.hdslb.com/bfs/archive/cover-reply.jpg","uri":"https://www.bilibili.com/opus/1073543151725051921#reply265141324256","native_uri":"bilibili://opus/detail/1073543151725051921?comment_root_id=265141324256&comment_on=1","detail_title":"","root_reply_content":"","source_content":"60","target_reply_content":"","at_details":[{"mid":2005337294,"fans":0,"nickname":"xyzhom","avatar":"https://i0.hdslb.com/bfs/face/member/noface.jpg","mid_link":"","follow":false}],"topic_details":[],"hide_reply_button":false,"hide_like_button":false,"like_state":0,"danmu":null,"message":""},"counts":1,"is_multi":0,"reply_time":1749474709}],"last_view_at":1749474724}}`

func msgfeedItems(t *testing.T, body string) []map[string]any {
	t.Helper()
	var env struct {
		Code int `json:"code"`
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := unmarshalUseNumber([]byte(body), &env); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return env.Data.Items
}

// authorNum reports whether a nested author/source map holds the expected
// numeric value under key k (numbers are json.Number after UseNumber decoding).
func authorNum(m map[string]any, k string, want int64) bool {
	got, ok := numAnyAsInt(m[k])
	return ok && got == want
}

func TestUnmarshalUnread(t *testing.T) {
	body := []byte(`{"code":0,"data":{"at":0,"chat":0,"coin":0,"danmu":0,"favorite":0,"like":222,"recv_like":222,"recv_reply":22,"reply":22,"sys_msg":2,"up":0}}`)
	var env envResp
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	u, err := unmarshalUnread(env.Data)
	if err != nil {
		t.Fatalf("unmarshalUnread: %v", err)
	}
	if u.Reply != 22 || u.At != 0 || u.Like != 222 || u.SysMsg != 2 {
		t.Fatalf("unexpected counts: %+v", u)
	}
	if u.Total != 246 {
		t.Fatalf("total: got %d want 246", u.Total)
	}
}

func TestParseMsgfeedReply(t *testing.T) {
	evs := parseMsgfeedItems(msgfeedItems(t, replyFeedFixture), "reply")
	if len(evs) != 1 {
		t.Fatalf("reply events: got %d want 1", len(evs))
	}
	ev := evs[0]
	if ev.Type != "reply" || ev.ID != 823260581625886 || ev.Ctime != 1749474709 {
		t.Fatalf("bad reply core: %+v", ev)
	}
	if ev.Uname != "佘总累了" || ev.Mid != 3546910497441845 {
		t.Fatalf("bad reply user: %+v", ev)
	}
	if ev.Message != "60" {
		t.Fatalf("bad reply message: %q", ev.Message)
	}
	if ev.Subject != "我已成为哔哩哔哩第245743680位转正会员" {
		t.Fatalf("bad reply subject: %q", ev.Subject)
	}
	if ev.DynamicURL != "https://www.bilibili.com/opus/1073543151725051921#reply265141324256" {
		t.Fatalf("bad reply url: %q", ev.DynamicURL)
	}
	if ev.Avatar != "https://i2.hdslb.com/bfs/face/e45c62bd47729e07dd01a788988be865ed3d210e.jpg" {
		t.Fatalf("bad reply avatar: %q", ev.Avatar)
	}
	if au := ev.Author; au == nil || !authorNum(au, "fans", 0) || au["follow"] != false {
		t.Fatalf("bad reply author object: %+v", ev.Author)
	}
	if ev.Image != "http://i0.hdslb.com/bfs/archive/cover-reply.jpg" || ev.Desc != "" {
		t.Fatalf("bad reply item image/desc: %+v", ev)
	}
	if ev.ItemType != "dynamic" || ev.Business != "动态" {
		t.Fatalf("bad reply item type/business: %+v", ev)
	}
	if ev.SubjectID != 1073543151725051921 || ev.SourceID != 265141324256 || ev.TargetID != 0 {
		t.Fatalf("bad reply ids: %+v", ev)
	}
	if ev.RootReply != "" || ev.TargetReply != "" {
		t.Fatalf("bad reply contents: %+v", ev)
	}
	if len(ev.AtDetails) != 1 || len(ev.TopicDetails) != 0 {
		t.Fatalf("bad reply details lists: %+v", ev)
	}
	if ev.Counts != 1 {
		t.Fatalf("bad reply counts: %+v", ev)
	}
}

func TestParseMsgfeedAtEmpty(t *testing.T) {
	evs := parseMsgfeedItems(msgfeedItems(t, `{"code":0,"data":{"cursor":{"is_end":true,"id":0,"time":0},"items":[]}}`), "at")
	if len(evs) != 0 {
		t.Fatalf("at events: got %d want 0", len(evs))
	}
}

func TestParseLikeItems(t *testing.T) {
	body := `{"code":0,"data":{"latest":{"items":[],"last_view_at":0},"total":{"cursor":{"is_end":false,"id":835383286005762,"time":1750919848},"items":[{"id":204473295385,"users":[{"mid":33767093,"fans":0,"nickname":"与风诉说我的温柔","avatar":"https://i2.hdslb.com/bfs/face/4bcbf848230774e58ea36fee83771d9dfb36683f.jpg","mid_link":"","follow":false},{"mid":3493087076682080,"fans":0,"nickname":"南边特产的瓜","avatar":"","mid_link":"","follow":false}],"item":{"item_id":440354885,"pid":0,"type":"video","business":"视频","business_id":1,"reply_business_id":0,"like_business_id":0,"title":"外景1","desc":"-","image":"http://i1.hdslb.com/bfs/archive/b8f3fbb1268fe4c761f0819793a702e072fe20b1.jpg","uri":"https://www.bilibili.com/video/BV1cL411f7er","detail_name":"查看视频详情","native_uri":"bilibili://video/440354885","ctime":1677723445},"counts":2,"like_time":1788420318,"notice_state":0}]}}}`
	var env struct {
		Code int `json:"code"`
		Data struct {
			Total struct {
				Items []map[string]any `json:"items"`
			} `json:"total"`
		} `json:"data"`
	}
	if err := unmarshalUseNumber([]byte(body), &env); err != nil {
		t.Fatalf("unmarshal like fixture: %v", err)
	}
	evs := parseLikeItems(env.Data.Total.Items)
	if len(evs) != 1 {
		t.Fatalf("like events: got %d want 1", len(evs))
	}
	ev := evs[0]
	if ev.Type != "like" || ev.ID != 204473295385 || ev.Ctime != 1788420318 {
		t.Fatalf("bad like core: %+v", ev)
	}
	if ev.Uname != "与风诉说我的温柔" || ev.Mid != 33767093 {
		t.Fatalf("bad like user: %+v", ev)
	}
	if ev.Message != "赞了你的内容" {
		t.Fatalf("bad like message: %q", ev.Message)
	}
	if ev.Subject != "外景1" || ev.DynamicURL != "https://www.bilibili.com/video/BV1cL411f7er" {
		t.Fatalf("bad like item: %+v", ev)
	}
	if ev.Avatar != "https://i2.hdslb.com/bfs/face/4bcbf848230774e58ea36fee83771d9dfb36683f.jpg" {
		t.Fatalf("bad like avatar: %q", ev.Avatar)
	}
	if au := ev.Author; au == nil || au["nickname"] != "与风诉说我的温柔" {
		t.Fatalf("bad like author object: %+v", ev.Author)
	}
	if ev.Counts != 2 {
		t.Fatalf("bad like like count: %+v", ev)
	}
	if ev.Image != "http://i1.hdslb.com/bfs/archive/b8f3fbb1268fe4c761f0819793a702e072fe20b1.jpg" || ev.Desc != "-" {
		t.Fatalf("bad like item image/desc: %+v", ev)
	}
	if ev.ItemType != "video" || ev.Business != "视频" {
		t.Fatalf("bad like item type/business: %+v", ev)
	}
	if ev.SubjectID != 440354885 || ev.ItemCtime != 1677723445 {
		t.Fatalf("bad like item id/ctime: %+v", ev)
	}
}

func TestParseSysItems(t *testing.T) {
	body := `{"code":0,"msg":"0","data":{"system_notify_list":[{"id":15708437,"cursor":1681725788589213193,"publisher":{"name":"","mid":0,"face":""},"type":4,"title":"活动奖励到账","content":"恭喜您在创作打卡活动中获奖","source":{"name":"","logo":"http://i2.hdslb.com/u_user/31ce8733288c19be92d7ce9cb83462dc.png"},"time_at":"2023-04-17 18:03:08","card_type":0,"card_brief":"","card_msg_brief":"","card_cover":"","card_story_title":"","card_link":"","mc":"1_17_11","is_station":0,"is_send":0,"notify_cursor":0}]}}`
	var env struct {
		Code int `json:"code"`
		Data struct {
			List []map[string]any `json:"system_notify_list"`
		} `json:"data"`
	}
	if err := unmarshalUseNumber([]byte(body), &env); err != nil {
		t.Fatalf("unmarshal sys fixture: %v", err)
	}
	evs := parseSysItems(env.Data.List)
	if len(evs) != 1 {
		t.Fatalf("sys events: got %d want 1", len(evs))
	}
	ev := evs[0]
	cst := time.FixedZone("CST", 8*3600)
	want := time.Date(2023, 4, 17, 18, 3, 8, 0, cst).Unix()
	if ev.Type != "sys" || ev.ID != 15708437 {
		t.Fatalf("bad sys core: %+v", ev)
	}
	if ev.Ctime != want {
		t.Fatalf("sys ctime: got %d want %d", ev.Ctime, want)
	}
	if ev.Subject != "活动奖励到账" || ev.Message != "恭喜您在创作打卡活动中获奖" {
		t.Fatalf("bad sys text: %+v", ev)
	}
	if au := ev.Author; au == nil || !authorNum(au, "mid", 0) {
		t.Fatalf("bad sys publisher object: %+v", ev.Author)
	}
	if ev.NotifyType != 4 || ev.CardType != 0 || ev.ItemType != "4" {
		t.Fatalf("bad sys types: %+v", ev)
	}
	if want := "http://i2.hdslb.com/u_user/31ce8733288c19be92d7ce9cb83462dc.png"; ev.Image != want {
		t.Fatalf("card_cover empty must fall back to source.logo: got %q want %q", ev.Image, want)
	}
	if src := ev.Source; src == nil || src["name"] != "" || src["logo"] != "http://i2.hdslb.com/u_user/31ce8733288c19be92d7ce9cb83462dc.png" {
		t.Fatalf("bad sys source object: %+v", ev.Source)
	}
}

func TestParseSysTime(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	want := time.Date(2023, 4, 17, 18, 3, 8, 0, cst).Unix()
	if got := parseSysTime("2023-04-17 18:03:08"); got != want {
		t.Fatalf("parseSysTime: got %d want %d", got, want)
	}
	if got := parseSysTime(""); got != 0 {
		t.Fatalf("parseSysTime empty: got %d want 0", got)
	}
	if got := parseSysTime(1234); got != 0 {
		t.Fatalf("parseSysTime non-string: got %d want 0", got)
	}
	if got := parseSysTime("not-a-time"); got != 0 {
		t.Fatalf("parseSysTime garbage: got %d want 0", got)
	}
}

func TestNotifyKeyParts(t *testing.T) {
	if got := notifyKey(&notifyEvent{Type: "sys", ID: 15708437}); got != "sys:15708437" {
		t.Fatalf("notifyKey: got %q", got)
	}
}
