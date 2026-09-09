package zhihu

// Zhihu hot list ("热榜"), the feed shown on zhihu.com/hot.
//
// Endpoint (verified 2026-09 to require a logged-in session; anonymous calls
// answer 101 AuthenticationError, consistent with RSSHub PR #19075):
//
//	GET /api/v3/feed/topstory/hot-lists/total?limit=50&mobile=true
//
// Requires the main z_c0 session like the notification feed; items are tracked
// by their feed id so a stable board only publishes newly-appeared entries.

import (
	"encoding/json"
	"fmt"
)

// hotlistURL mirrors the zhihu-plus-plus client's query (mobile=true), the
// authoritative same-source counterpart of our signing implementation.
const hotlistURL = "https://www.zhihu.com/api/v3/feed/topstory/hot-lists/total?limit=50&mobile=true"

// HotlistItem is one entry of the hot list. The target payload shape varies
// (question/pin/topic…) so it is kept as raw JSON and only lightly inspected.
type HotlistItem struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Detail string          `json:"detail_text,omitempty"`
	Target json.RawMessage `json:"target"`
}

// HotlistResp is the response envelope of the hot list endpoint.
type HotlistResp struct {
	Data      []HotlistItem `json:"data"`
	FreshText string        `json:"fresh_text,omitempty"`
}

// FetchHotlist fetches the current hot list with the shared session.
func FetchHotlist() (*HotlistResp, error) {
	var r HotlistResp
	if err := getJSON(&r, hotlistURL, true); err != nil {
		return nil, fmt.Errorf("zhihu: hotlist: %w", err)
	}
	return &r, nil
}

// areaText reads the .text of a named *_area block (title_area, excerpt_area,
// metrics_area…), the shape the web payload has used since ~2024.
func areaText(m map[string]any, key string) string {
	a, ok := m[key].(map[string]any)
	if !ok {
		return ""
	}
	if s, ok := a["text"].(string); ok {
		return s
	}
	return ""
}

// HotlistTitle extracts a readable short title from a raw target. It prefers
// the title_area.text block and falls back to the older direct fields.
func HotlistTitle(raw json.RawMessage) string {
	m := targetMap(raw)
	if s := areaText(m, "title_area"); s != "" {
		return s
	}
	for _, k := range []string{"title", "name", "excerpt"} {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case map[string]any:
			if s, ok := v["title"].(string); ok && s != "" {
				return s
			}
		}
	}
	if q, ok := m["question"].(map[string]any); ok {
		if s, ok := q["title"].(string); ok && s != "" {
			return s
		}
	}
	// detail_text is the last-resort title for targets with no real title
	// (e.g. pins).
	if s, ok := m["detail_text"].(string); ok && s != "" {
		return s
	}
	return ""
}

// HotlistDetail extracts the one-line heat/discussion summary of a target, if
// any: excerpt_area/metrics_area blocks first, then the older direct fields.
func HotlistDetail(raw json.RawMessage) string {
	m := targetMap(raw)
	for _, k := range []string{"excerpt_area", "metrics_area"} {
		if s := areaText(m, k); s != "" {
			return s
		}
	}
	for _, k := range []string{"detail_text", "excerpt", "title"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// HotlistLink returns the target's own link (target.link.url) verbatim, as
// given by the hot list API, without any path/id extraction.
func HotlistLink(raw json.RawMessage) string {
	m := targetMap(raw)
	link, ok := m["link"].(map[string]any)
	if !ok {
		return ""
	}
	u, _ := link["url"].(string)
	return u
}

func targetMap(raw json.RawMessage) map[string]any {
	var m map[string]any
	if len(raw) == 0 {
		return m
	}
	_ = json.Unmarshal(raw, &m)
	return m
}
