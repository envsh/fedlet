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
	"bytes"
	"encoding/json"
	"fmt"
)

// hotlistURL mirrors the zhihu-plus-plus client's query (mobile=true), the
// authoritative same-source counterpart of our signing implementation.
const hotlistURL = "https://www.zhihu.com/api/v3/feed/topstory/hot-lists/total?limit=50&mobile=true"

// HotlistItem is one entry of the hot list. The target payload shape varies
// (question/pin/topic…) so it is kept as raw JSON and only lightly inspected.
type HotlistItem struct {
	ID        string          `json:"id"`
	CardID    string          `json:"card_id"`
	Type      string          `json:"type"`
	StyleType string          `json:"style_type,omitempty"`
	Detail    string          `json:"detail_text,omitempty"`
	Trend     int             `json:"trend,omitempty"`
	Debut     bool            `json:"debut,omitempty"`
	Target    json.RawMessage `json:"target"`
	Children  []HotlistChild  `json:"children,omitempty"`
}

// HotlistChild is one element of a hot feed's children array. On the hot board
// its thumbnail is the entry cover: the first answer's thumbnail for a
// question, the article cover for an article (zhihu-plus-plus HotListScreen
// renders children.firstOrNull().thumbnail).
type HotlistChild struct {
	Type      string `json:"type"`
	Thumbnail string `json:"thumbnail"`
}

// HotlistResp is the response envelope of the hot list endpoint.
type HotlistResp struct {
	Data      []HotlistItem `json:"data"`
	FreshText string        `json:"fresh_text,omitempty"`
}

// HotlistCardID returns the stable card id of a hot list entry, if the API
// sends one (e.g. "Q_2052718434220073058"). It is the primary dedupe key.
func HotlistCardID(it *HotlistItem) string {
	return it.CardID
}

// HotlistTargetID extracts the content id of the hot list target (target.id)
// as a string, whether the API sends a number or a string wrapper. The raw
// JSON is kept verbatim so 64-bit ids (e.g. 2052718434220073058) do not lose
// precision through float64.
func HotlistTargetID(raw json.RawMessage) string {
	var t struct {
		ID json.RawMessage `json:"id"`
	}
	if len(raw) == 0 {
		return ""
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return ""
	}
	b := bytes.TrimSpace(t.ID)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) || b[0] == '{' || b[0] == '[' {
		return ""
	}
	return string(bytes.Trim(b, `"`))
}

// HotlistKey returns the stable identity of a hot list entry for deduplication.
// The feed wrapper id (data[].id) is a per-request volatile value like
// "0_1782366660.747819" ("<rank>_<unix_ms>"), so it must not be used as the
// dedupe key. card_id wins, then the target's own content id. An entry with
// neither has no stable identity: ok=false and the publisher must skip it.
func HotlistKey(it *HotlistItem) (string, bool) {
	if k := HotlistCardID(it); k != "" {
		return k, true
	}
	if k := HotlistTargetID(it.Target); k != "" {
		return k, true
	}
	return "", false
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

// HotlistDetailText returns the feed's one-line heat summary. The feed-level
// detail_text ("899 万热度") wins; the target excerpt/metrics area is the
// fallback for payload shapes that carry no feed detail.
func HotlistDetailText(it *HotlistItem) string {
	if it == nil {
		return ""
	}
	if it.Detail != "" {
		return it.Detail
	}
	return HotlistDetail(it.Target)
}

// HotlistImage returns the entry cover: the newer image_area.url when present,
// else children[0].thumbnail (answer thumbnail for a question, article cover
// for an article), else the target's own thumbnail(s).
func HotlistImage(it *HotlistItem) string {
	if it == nil {
		return ""
	}
	m := targetMap(it.Target)
	if a, ok := m["image_area"].(map[string]any); ok {
		if u, _ := a["url"].(string); u != "" {
			return u
		}
	}
	for _, c := range it.Children {
		if c.Thumbnail != "" {
			return c.Thumbnail
		}
	}
	if u, _ := m["thumbnail"].(string); u != "" {
		return u
	}
	if ts, ok := m["thumbnails"].([]any); ok {
		for _, v := range ts {
			if u, _ := v.(string); u != "" {
				return u
			}
		}
	}
	return ""
}

// HotlistAuthor returns the target's author object verbatim (target.author).
// Question targets carry a placeholder ("用户", empty url_token); it is
// published as-is by request.
func HotlistAuthor(it *HotlistItem) map[string]any {
	if it == nil {
		return nil
	}
	m := targetMap(it.Target)
	a, _ := m["author"].(map[string]any)
	return a
}

// HotlistLink returns the target's own link (target.link.url) verbatim, as
// given by the hot list API, without any path/id extraction. When the payload
// has no link block (the live mobile hot-board shape), target.url is used.
func HotlistLink(raw json.RawMessage) string {
	m := targetMap(raw)
	if link, ok := m["link"].(map[string]any); ok {
		if u, _ := link["url"].(string); u != "" {
			return u
		}
	}
	u, _ := m["url"].(string)
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
