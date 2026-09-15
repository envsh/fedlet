package bilibili

import (
	"encoding/json"
	"testing"
)

func TestParseHistoryCursor(t *testing.T) {
	body := `{"code":0,"data":{"cursor":{"max":117240510809627,"view_at":1789261238,"business":"archive","ps":30},"tab":[],"list":[{"kid":265141324256,"title":"外景1","cover":"http://i0.hdslb.com/bfs/archive/x.jpg","uri":"https://www.bilibili.com/video/BV1cL411f7er","history":{"oid":1,"epid":0,"bvid":"BV1cL411f7er","page":1,"cid":9,"part":"外景1","business":"archive","dt":3},"author_name":"与风诉说我的温柔","author_mid":33767093,"view_at":1749474709,"progress":80,"duration":264,"badge":"","show_title":""}]}}`
	var data histData
	if err := json.Unmarshal(envelopeData(t, body), &data); err != nil {
		t.Fatalf("unmarshal history data: %v", err)
	}
	if data.Cursor.Max != 117240510809627 || data.Cursor.Business != "archive" ||
		data.Cursor.ViewAt != 1789261238 || data.Cursor.PS != 30 {
		t.Fatalf("bad cursor: %+v", data.Cursor)
	}
	if len(data.List) != 1 {
		t.Fatalf("list: got %d want 1", len(data.List))
	}
	it := data.List[0]
	if it.Kid != 265141324256 || it.ViewAt != 1749474709 || it.History.Business != "archive" {
		t.Fatalf("bad history core: %+v", it)
	}
	if it.Title != "外景1" || it.AuthorName != "与风诉说我的温柔" || it.AuthorMid != 33767093 {
		t.Fatalf("bad history text: %+v", it)
	}
	if it.Duration != 264 || it.Progress != 80 {
		t.Fatalf("bad history progress: %+v", it)
	}
	if got := histItemID(&it); got != "archive:265141324256" {
		t.Fatalf("histItemID: got %q", got)
	}
	if got := histURLFor(&it); got != "https://www.bilibili.com/video/BV1cL411f7er" {
		t.Fatalf("histURLFor: got %q", got)
	}
}

func TestHistoryURITypes(t *testing.T) {
	live := histItem{Kid: 1, URI: "https://live.bilibili.com/123"}
	live.History.Business = "live"
	if got := histURLFor(&live); got != "https://live.bilibili.com/123" {
		t.Fatalf("histURLFor live: got %q", got)
	}
	art := histItem{}
	art.History.Business = "archive"
	art.History.Bvid = "BV1x"
	if got := histURLFor(&art); got != "https://www.bilibili.com/video/BV1x" {
		t.Fatalf("histURLFor archive fallback: got %q", got)
	}
	other := histItem{}
	other.History.Business = "pgc"
	if got := histURLFor(&other); got != "" {
		t.Fatalf("histURLFor pgc fallback: got %q", got)
	}
}
