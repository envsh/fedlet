package xhs

// Home feed ("首页推荐流"), read from the homefeed endpoint.
//
// POST /api/sns/web/v1/homefeed with a category selects the stream:
// "homefeed_recommend" is the default recommendation feed and
// "homefeed.fashion_v3" / "homefeed.food_v3" & co select the sub-channels.
// The feed answers with a guest session (login/activate); it does not require
// real login. Items are tracked by note id. The (superseded) hot board used to
// be this endpoint with the "homefeed.fashion_v3" category; the real hot board
// (热搜榜) lives in hotlist.go and is fetched from an anonymous aggregator,
// because xhs serves no signed hot-board API on edith/www (probing 2026-09:
// edith has no hsearch/trending route and the www gateway rejects every
// request shape).
//
// Note: the body must keep this exact key order — the signature is computed
// over the raw body string.

import "fmt"

// homefeedURL is the homefeed endpoint; the stream is selected by the body.
const homefeedURL = "/api/sns/web/v1/homefeed"

// homefeedBody builds the exact POST body for a homefeed category, mirroring
// the web client (image_scenes as a JSON array). An empty category falls back
// to the default recommendation feed.
func homefeedBody(category string) string {
	if category == "" {
		category = "homefeed_recommend"
	}
	return `{"cursor_score":"","num":40,"refresh_type":1,"note_index":0,` +
		`"unread_begin_note_id":"","unread_end_note_id":"","unread_note_count":0,` +
		`"category":"` + category + `","search_key":"","need_num":40,` +
		`"image_scenes":["FD_PRV_WEBP","FD_WM_WEBP"]}`
}

// HomefeedItem is one entry of the recommendation feed. The web payload wraps
// notes in a "note_card"; the raw shape is kept as-is and only lightly
// inspected.
type HomefeedItem struct {
	ID     string         `json:"id"`
	Type   string         `json:"type"`
	Card   map[string]any `json:"note_card"`
	User   map[string]any `json:"user"`
	Raw    map[string]any `json:"-"`
	Rank   int            `json:"rank"`
	NoteID string         `json:"note_id"`
}

// HomefeedResp is the response envelope of the recommendation feed.
type HomefeedResp struct {
	Data []HomefeedItem `json:"data"`
}

// FetchHomefeed pulls one page of the homefeed for the given category (empty
// means "homefeed_recommend") via the shared signed client. It answers with a
// guest session too (login/activate).
func FetchHomefeed(category string) (*HomefeedResp, error) {
	c := client()
	data, status, err := c.exec(signHeadersOptions{
		method:   "POST",
		uri:      homefeedURL,
		body:     homefeedBody(category),
		ts:       float64AtNow(),
		withXRap: true,
	})
	if err != nil {
		return nil, fmt.Errorf("xhs: homefeed: %w", err)
	}
	out, status, err := decodeXHSPayload(data, status)
	if err != nil {
		return nil, fmt.Errorf("xhs: homefeed: %w", err)
	}
	return &HomefeedResp{Data: parseHomefeedItems(out)}, nil
}

// parseHomefeedItems flattens the homefeed "items" array into HomefeedItems,
// tolerantly reading note_card/user blocks with whatever fields exist.
func parseHomefeedItems(out map[string]any) []HomefeedItem {
	itemsRaw, _ := out["items"].([]any)
	items := make([]HomefeedItem, 0, len(itemsRaw))
	for i, it := range itemsRaw {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		noteID := id
		typ, _ := m["type"].(string)
		var card map[string]any
		if c, ok := m["note_card"].(map[string]any); ok {
			card = c
			if nid, ok := c["id"].(string); ok && nid != "" {
				noteID = nid
			}
		}
		var user map[string]any
		if u, ok := m["user"].(map[string]any); ok {
			user = u
		}
		items = append(items, HomefeedItem{
			ID:     id,
			Type:   typ,
			Card:   card,
			User:   user,
			Raw:    m,
			Rank:   i,
			NoteID: noteID,
		})
	}
	return items
}

// HomefeedTitle extracts the note title from the note_card block.
func HomefeedTitle(it *HomefeedItem) string {
	if it == nil {
		return ""
	}
	if it.Card != nil {
		for _, k := range []string{"display_title", "title"} {
			if s, ok := it.Card[k].(string); ok && s != "" {
				return s
			}
		}
	}
	if s, ok := it.Raw["title"].(string); ok {
		return s
	}
	return ""
}

// HomefeedDetail extracts engagement counts (likes/collects/comments) from the
// interact_info block, if present.
func HomefeedDetail(it *HomefeedItem) string {
	if it == nil {
		return ""
	}
	info, ok := it.Card["interact_info"].(map[string]any)
	if !ok {
		return ""
	}
	format := func(key string) string {
		v, _ := info[key].(float64)
		return formatCount(v)
	}
	return "👍 " + format("liked_count") +
		" ⭐ " + format("collected_count") +
		" 💬 " + format("comment_count")
}

// HomefeedLink returns the web explore URL for the note.
func HomefeedLink(it *HomefeedItem) string {
	if it == nil || it.NoteID == "" {
		return ""
	}
	return "https://www.xiaohongshu.com/explore/" + it.NoteID
}

// formatCount renders big engagement numbers compactly (98765 -> 9.9w).
func formatCount(v float64) string {
	if v == 0 {
		return "0"
	}
	n := int64(v)
	if n >= 10000 {
		return fmt.Sprintf("%.1fw", float64(n)/10000)
	}
	return fmt.Sprintf("%d", n)
}