package zhihu

// Zhihu recommendation feed ("推荐流"), the personalized home-page stream.
//
// Endpoint (same-source as zhihu-plus-plus, which is our signing reference):
//
//	GET /api/v3/feed/topstory/recommend?desktop=true&limit=N
//
// Requires the main z_c0 session like the hot list (anonymous calls answer 403
// risk control, verified 2026-09). It is a linear, non-ranked stream: items
// are a mix of answers, articles, zvideos, questions, pins and ad slots, each
// carrying a `target` payload whose shape varies by `type`.
//
// This is a pull-only layer: FetchRecommend is deliberately NOT wired into the
// poll loop (no dedupe state, no auto publish, no fedbridge hook). Callers
// invoke it on demand; without a valid session it fails fast with
// ErrNotLoggedIn and never touches the login gateway.

import (
	"encoding/json"
	"fmt"
)

const (
	recommendBaseURL      = "https://www.zhihu.com/api/v3/feed/topstory/recommend?desktop=true&limit=%d"
	defaultRecommendLimit = 20
)

// RecommendItem is one raw element of the recommendation stream. The target
// payload varies by type, so it is kept as raw JSON and lightly inspected.
type RecommendItem struct {
	ID          int64           `json:"id"`
	Type        string          `json:"type"`
	Target      json.RawMessage `json:"target"`
	CreatedTime int64           `json:"created_time"`
}

// RecommendPaging is the stream cursor (only the first page is surfaced here).
type RecommendPaging struct {
	IsEnd    bool   `json:"is_end"`
	Next     string `json:"next"`
	Previous string `json:"previous"`
}

// RecommendResp is the response envelope of the recommendation endpoint.
type RecommendResp struct {
	Data      []RecommendItem `json:"data"`
	Paging    RecommendPaging `json:"paging"`
	FreshText string          `json:"fresh_text"`
}

// RecommendContent is the rich, parsed view of one feed item.
type RecommendContent struct {
	ID           int64           `json:"id"`
	Kind         string          `json:"kind"`
	Title        string          `json:"title"`
	Excerpt      string          `json:"excerpt"`
	Summary      string          `json:"summary"`
	URL          string          `json:"url"`
	Author       string          `json:"author"`
	VoteupCount  int64           `json:"voteup_count"`
	CommentCount int64           `json:"comment_count"`
	CreatedTime  int64           `json:"created_time"`
	Target       json.RawMessage `json:"target"`
}

// FetchRecommend fetches the first page of the recommendation stream. limit<=0
// means the default (20). Unlike the polled feeds this only returns data: no
// publish, no dedupe state, and a lost session is reported to the caller
// instead of routing into the login gateway.
func FetchRecommend(limit int) (*RecommendResp, error) {
	if limit <= 0 {
		limit = defaultRecommendLimit
	}
	u := fmt.Sprintf(recommendBaseURL, limit)
	var r RecommendResp
	if err := getJSON(&r, u, true); err != nil {
		return nil, err
	}
	return &r, nil
}

// RecommendView builds the rich view of one feed item without touching the
// network. Ad slots and other empty-target items yield a zero-value view whose
// Kind may still identify them.
func RecommendView(it *RecommendItem) RecommendContent {
	c := RecommendContent{
		ID:          it.ID,
		Kind:        it.Type,
		CreatedTime: it.CreatedTime,
		Target:      it.Target,
	}
	t := targetMap(it.Target)
	if len(t) == 0 {
		return c
	}
	if c.Kind == "" {
		c.Kind, _ = t["type"].(string)
	}
	if v, ok := t["id"].(float64); ok {
		c.ID = int64(v)
	}
	if c.ID == 0 {
		c.ID = it.ID
	}
	c.Title = recommendTitle(t)
	c.Excerpt = strField(t, "excerpt")
	c.Author = recommendAuthor(t)
	c.VoteupCount = intField(t, "voteup_count")
	c.CommentCount = intField(t, "comment_count")
	c.Summary = recommendSummary(t)
	c.URL = recommendLink(t)
	return c
}

// recommendTitle extracts the item title by target type.
func recommendTitle(t map[string]any) string {
	if q, ok := t["question"].(map[string]any); ok {
		if s := strField(q, "title"); s != "" {
			return s
		}
	}
	switch t["type"] {
	case "zvideo", "article", "org":
		if s := strField(t, "title"); s != "" {
			return s
		}
	case "pin":
		if s := strField(t, "content"); s != "" {
			return stripHTMLSummary(s)
		}
	}
	for _, k := range []string{"title", "name"} {
		if s := strField(t, k); s != "" {
			return s
		}
	}
	return ""
}

// recommendAuthor extracts the contributing author name.
func recommendAuthor(t map[string]any) string {
	a, ok := t["author"].(map[string]any)
	if !ok {
		return ""
	}
	return strField(a, "name")
}

// recommendSummary extracts a readable content summary depending on type:
// answer/zvideo/pin carry body HTML, other types fall back to their excerpt.
func recommendSummary(t map[string]any) string {
	switch t["type"] {
	case "answer", "zvideo", "pin":
		s := strField(t, "content")
		if s != "" {
			return stripHTMLSummary(s)
		}
	}
	if s := strField(t, "excerpt"); s != "" {
		return stripHTMLSummary(s)
	}
	return ""
}

// recommendLink returns the item's own URL, with an answer-specific fallback
// built from the question and answer ids if target.url is missing.
func recommendLink(t map[string]any) string {
	if u := strField(t, "url"); u != "" {
		return u
	}
	if t["type"] == "answer" {
		if q, ok := t["question"].(map[string]any); ok {
			return fmt.Sprintf("https://www.zhihu.com/question/%v/answer/%v", q["id"], t["id"])
		}
	}
	return ""
}

func strField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func intField(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}
