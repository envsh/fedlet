package zhihu

import (
	"encoding/json"
	"testing"
)

func TestParseHistoryEntries(t *testing.T) {
	body := `{"data":[{"card_type":"content","data":{"id":"aaa","header":{"title":"某问题"},"content":{"author_name":"作者乙","summary":"第一段摘要","cover_image":"http://x/pic.jpg"},"matrix":{},"action":{"type":"answer","url":"https://www.zhihu.com/question/1/answer/2"},"extra":{"content_token":"2","content_type":"answer","question_token":"1","read_time":"1749474709"}}},{"card_type":"content","data":{"id":"bbb","content":{"author_name":"作者丙","summary":"文章摘要"},"extra":{"content_token":"p-1","content_type":"article","read_time":"1749473000"}}}],"paging":{"is_end":true,"totals":2}}`
	var r struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("unmarshal history fixture: %v", err)
	}
	items := make([]HistoryEntry, 0, len(r.Data))
	for i := range r.Data {
		items = append(items, parseHistoryEntry(r.Data[i]))
	}
	if len(items) != 2 {
		t.Fatalf("entries: got %d want 2", len(items))
	}
	a := items[0]
	if a.ContentType != "answer" || a.ContentToken != "2" || a.QuestionToken != "1" {
		t.Fatalf("bad answer history: %+v", a)
	}
	if a.Author != "作者乙" || a.Summary != "第一段摘要" || a.Cover != "http://x/pic.jpg" {
		t.Fatalf("bad answer content: %+v", a)
	}
	if a.URL != "https://www.zhihu.com/question/1/answer/2" {
		t.Fatalf("answer url: got %q", a.URL)
	}
	if got := historyItemKey(&a); got != "answer:2" {
		t.Fatalf("historyItemKey: got %q", got)
	}
	art := items[1]
	if art.ContentType != "article" || art.ContentToken != "p-1" || art.Author != "作者丙" {
		t.Fatalf("bad article history: %+v", art)
	}
	if art.URL != "https://zhuanlan.zhihu.com/p/p-1" {
		t.Fatalf("article url fallback: got %q", art.URL)
	}
}

func TestHistoryKeyEmpty(t *testing.T) {
	e := &HistoryEntry{}
	if got := historyItemKey(e); got != ":" {
		t.Fatalf("empty historyItemKey: got %q", got)
	}
}
