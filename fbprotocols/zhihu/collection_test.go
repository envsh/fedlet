package zhihu

import (
	"encoding/json"
	"testing"
)

func TestParseCollectionContents(t *testing.T) {
	body := `{"data":[{"id":88998877,"type":"answer","created_time":1749474709,"updated_time":1749474709,"title":"如何评价今天的开盘?","excerpt":"大面一碗。","author":{"name":"段子手","id":"xyz"},"question":{"id":123456,"title":"如何评价今天的开盘?"},"voteup_count":42},{"id":6677,"attachment":{"type":"ARTICLE"},"created_time":1749474000,"title":"我的专栏","excerpt":"","author":{"name":"作者甲"},"url":"https://zhuanlan.zhihu.com/p/6677","voteup_count":1}],"paging":{"is_end":true,"totals":2}}`
	var r struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("unmarshal contents fixture: %v", err)
	}
	items := make([]CollectionItem, 0, len(r.Data))
	for i := range r.Data {
		items = append(items, parseCollectionItem(r.Data[i]))
	}
	if len(items) != 2 {
		t.Fatalf("items: got %d want 2", len(items))
	}
	a := items[0]
	if a.ID != 88998877 || a.ContentType != "answer" || a.Author != "段子手" {
		t.Fatalf("bad answer item: %+v", a)
	}
	if a.QuestionID != 123456 || a.VoteupCount != 42 {
		t.Fatalf("bad answer extras: %+v", a)
	}
	if a.Title != "如何评价今天的开盘?" {
		t.Fatalf("answer title: got %q", a.Title)
	}
	if a.URL != "https://www.zhihu.com/question/123456/answer/88998877" {
		t.Fatalf("answer url: got %q", a.URL)
	}
	art := items[1]
	if art.ID != 6677 || art.ContentType != "article" || art.URL != "https://zhuanlan.zhihu.com/p/6677" {
		t.Fatalf("bad article item: %+v", art)
	}
}

func TestCollectionItemKey(t *testing.T) {
	ce := &CollectionEntry{CollectionID: 42, Item: CollectionItem{ID: 88998877}}
	if got := collectionItemKey(ce); got != "42:88998877" {
		t.Fatalf("collectionItemKey: got %q", got)
	}
}
