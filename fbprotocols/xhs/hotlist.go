package xhs

// Hot feed ("热榜"), read from the homefeed endpoint with the hot category.
//
// POST /api/sns/web/v1/homefeed with category "homefeed.fashion_v3" returns
// the trending board. Works with a guest session (login/activate), so the hot
// feed does not require real login. Payload shape mirrors the jackwener
// client (verified 2026-09); items are tracked by note id and only new ones
// are published.
//
// Note: the body must keep this exact key order — the signature is computed
// over the raw body string.

import "fmt"

// hotlistURL is the homefeed endpoint; the hot board is selected by payload.
const hotlistURL = "/api/sns/web/v1/homefeed"

// hotfeedBody is the exact POST body for the hot feed, mirroring the web
// client (image_scenes as a JSON array).
const hotfeedBody = `{"cursor_score":"","num":40,"refresh_type":1,"note_index":0,` +
	`"unread_begin_note_id":"","unread_end_note_id":"","unread_note_count":0,` +
	`"category":"homefeed.fashion_v3","search_key":"","need_num":40,` +
	`"image_scenes":["FD_PRV_WEBP","FD_WM_WEBP"]}`

// HotlistItem is one entry of the hot board. The web payload wraps notes in a
// "note_card"; the raw shape is kept as-is and only lightly inspected.
type HotlistItem struct {
	ID     string         `json:"id"`
	Type   string         `json:"type"`
	Card   map[string]any `json:"note_card"`
	User   map[string]any `json:"user"`
	Raw    map[string]any `json:"-"`
	Rank   int            `json:"rank"`
	NoteID string         `json:"note_id"`
}

// HotlistResp is the response envelope of the hot board.
type HotlistResp struct {
	Data []HotlistItem `json:"data"`
}

// FetchHotlist fetches the current hot board via the shared signed client. It
// answers with a guest session too (login/activate).
func FetchHotlist() (*HotlistResp, error) {
	c := client()
	data, status, err := c.exec(signHeadersOptions{
		method:   "POST",
		uri:      hotlistURL,
		body:     hotfeedBody,
		ts:       float64AtNow(),
		withXRap: true,
	})
	if err != nil {
		return nil, fmt.Errorf("xhs: hotlist: %w", err)
	}
	out, status, err := decodeXHSPayload(data, status)
	if err != nil {
		return nil, fmt.Errorf("xhs: hotlist: %w", err)
	}
	return &HotlistResp{Data: parseHotlistItems(out)}, nil
}

// parseHotlistItems flattens the homefeed "items" array into HotlistItems,
// tolerantly reading note_card/user blocks with whatever fields exist.
func parseHotlistItems(out map[string]any) []HotlistItem {
	itemsRaw, _ := out["items"].([]any)
	items := make([]HotlistItem, 0, len(itemsRaw))
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
		items = append(items, HotlistItem{
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

// HotlistTitle extracts the note title from the note_card block.
func HotlistTitle(it *HotlistItem) string {
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

// HotlistDetail extracts engagement counts (likes/collects/comments) from the
// interact_info block, if present.
func HotlistDetail(it *HotlistItem) string {
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

// HotlistLink returns the web explore URL for the note.
func HotlistLink(it *HotlistItem) string {
	if it == nil || it.NoteID == "" {
		return ""
	}
	return "https://www.xiaohongshu.com/explore/" + it.NoteID
}

// formatCount renders big engagement numbers compactly (1234 -> 1234).
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
