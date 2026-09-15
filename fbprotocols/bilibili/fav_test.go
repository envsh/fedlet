package bilibili

import (
	"encoding/json"
	"testing"
)

// envelopeData extracts the {"data": ...} payload of a {code,...} envelope,
// mirroring how getJSON decodes its typed target.
func envelopeData(t *testing.T, body string) []byte {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	return env.Data
}

func TestParseFavFolders(t *testing.T) {
	body := `{"code":0,"data":{"list":[{"id":1117882647,"fid":0,"mid":443290258,"attr":0,"title":"默认收藏夹","media_count":1},{"id":1287831957,"title":"要看的","media_count":232}]}}`
	var data favFoldersData
	if err := json.Unmarshal(envelopeData(t, body), &data); err != nil {
		t.Fatalf("unmarshal folders data: %v", err)
	}
	if len(data.List) != 2 {
		t.Fatalf("folders: got %d want 2", len(data.List))
	}
	if data.List[0].ID != 1117882647 || data.List[0].Title != "默认收藏夹" {
		t.Fatalf("bad folder 0: %+v", data.List[0])
	}
	if data.List[1].ID != 1287831957 || data.List[1].MediaCount != 232 {
		t.Fatalf("bad folder 1: %+v", data.List[1])
	}
}

func TestParseFavMedia(t *testing.T) {
	body := `{"code":0,"data":{"count":1,"medias":[{"id":369169315,"type":2,"bvid":"BV1cL411f7er","fav_time":1749474709,"title":"外景1","cover":"http://i0.hdslb.com/bfs/archive/x.jpg","upper":{"mid":33767093,"name":"与风诉说我的温柔"}}],"has_more":false}}`
	var data favResourcesData
	if err := json.Unmarshal(envelopeData(t, body), &data); err != nil {
		t.Fatalf("unmarshal media data: %v", err)
	}
	if len(data.Medias) != 1 {
		t.Fatalf("medias: got %d want 1", len(data.Medias))
	}
	it := data.Medias[0]
	if it.ID != 369169315 || it.FavTime != 1749474709 || it.Title != "外景1" {
		t.Fatalf("bad media: %+v", it)
	}
	if it.Upper.Name != "与风诉说我的温柔" || it.Upper.Mid != 33767093 {
		t.Fatalf("bad media upper: %+v", it.Upper)
	}
	if got := favMediaID(1287831957, &it); got != "1287831957:369169315" {
		t.Fatalf("favMediaID: got %q", got)
	}
	if got := favPath(&it); got != "https://www.bilibili.com/video/BV1cL411f7er" {
		t.Fatalf("favPath: got %q", got)
	}
}

func TestFavPathFallback(t *testing.T) {
	it := favMedia{Bvid: "", ID: 369169315}
	if got := favPath(&it); got != "https://www.bilibili.com/video/av369169315" {
		t.Fatalf("favPath fallback: got %q", got)
	}
}
