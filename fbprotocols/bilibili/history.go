package bilibili

// Watch history (x/web-interface/history/cursor). Needs SESSDATA. Publishes
// newly-watched entries only (dedupe by business+kid).
// Publish: each entry forwarded verbatim + flat proto_type/cycle_count (top level).
//
// The endpoint paginates with an opaque cursor (max/business/view_at echoed
// back). The per-item `business` lives nested under history.business, and the
// wire carries no is_end — the walk stops on an empty page or a hard cap.
// Bilibili keeps only roughly the last three months of history server-side.
//
// Verified live 2026-09 against the production response shape.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

const (
	historyURLBase = apiHost + "/x/web-interface/history/cursor?ps=30"
	histMaxPages   = 10
)

// histCursor is the opaque continuation token the endpoint requires.
type histCursor struct {
	Max      int64  `json:"max"`
	ViewAt   int64  `json:"view_at"`
	Business string `json:"business"`
	PS       int    `json:"ps"`
}

// histItem is the per-entry shape of history/cursor.data.list.
type histItem struct {
	Kid        int64  `json:"kid"`
	ViewAt     int64  `json:"view_at"`
	Title      string `json:"title"`
	Cover      string `json:"cover"`
	URI        string `json:"uri"`
	AuthorName string `json:"author_name"`
	AuthorMid  int64  `json:"author_mid"`
	Badge      string `json:"badge"`
	ShowTitle  string `json:"show_title"`
	Duration   int64  `json:"duration"`
	Progress   int64  `json:"progress"`
	History    struct {
		Oid      int64  `json:"oid"`
		Epid     int64  `json:"epid"`
		Bvid     string `json:"bvid"`
		Business string `json:"business"`
	} `json:"history"`
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON keeps the original entry bytes for verbatim forwarding.
func (it *histItem) UnmarshalJSON(b []byte) error {
	type alias histItem
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*it = histItem(a)
	it.Raw = append(json.RawMessage(nil), b...)
	return nil
}

type histData struct {
	Cursor histCursor `json:"cursor"`
	List   []histItem `json:"list"`
}

// histItemID returns the dedupe key (business+kid: re-watching a video keeps
// the same kid and should not re-publish).
func histItemID(it *histItem) string {
	return fmt.Sprintf("%s:%d", it.History.Business, it.Kid)
}

// fetchHistoryPage fetches one page; the empty cursor fetches the first page.
func fetchHistoryPage(c histCursor) (histData, error) {
	u := historyURLBase
	if c.Max != 0 {
		u += fmt.Sprintf("&max=%d&business=%s&view_at=%d", c.Max, url.QueryEscape(c.Business), c.ViewAt)
	}
	var data histData
	if err := getJSON(&data, u, true); err != nil {
		return data, err
	}
	return data, nil
}

// FetchHistory fetches the recent watch history, newest-first, walking the
// cursor until an empty page or the page cap.
func FetchHistory() ([]histItem, error) {
	ensureWarmup()
	if jar.get("SESSDATA") == "" {
		return nil, ErrNotLoggedIn
	}
	var out []histItem
	cur := histCursor{}
	for page := 0; page < histMaxPages; page++ {
		d, err := fetchHistoryPage(cur)
		if err != nil {
			return nil, err
		}
		out = append(out, d.List...)
		if len(d.List) == 0 {
			break
		}
		cur = d.Cursor
	}
	return out, nil
}

// histURLFor builds a deep link for a history entry (the API's own uri wins).
func histURLFor(it *histItem) string {
	if it.URI != "" {
		return it.URI
	}
	if it.History.Business == "archive" && it.History.Bvid != "" {
		return "https://www.bilibili.com/video/" + it.History.Bvid
	}
	return ""
}

// historyRound fetches + publishes newly-watched entries. Returns the number
// published (0 on first sync: the existing history is seeded silently).
func historyRound(state *biliState) int {
	items, err := FetchHistory()
	if err != nil {
		log.Printf("bilibili: history error: %v", err)
		pushError(err)
		return 0
	}
	now := time.Now()
	if len(state.History) == 0 {
		for i := range items {
			state.History[histItemID(&items[i])] = now.Unix()
		}
		log.Printf("bilibili: history first sync, seeded %d existing entries", len(items))
		return 0
	}
	published := 0
	for i := range items {
		it := &items[i]
		key := histItemID(it)
		if _, seen := state.History[key]; seen {
			continue
		}
		state.History[key] = now.Unix()
		log.Printf("bilibili: history %s:%d %s", it.History.Business, it.Kid, truncate(it.Title, 60))
		payload := map[string]any{
			"kind":         "bilibili_history",
			"id":           it.Kid,
			"business":     it.History.Business,
			"title":        it.Title,
			"cover":        it.Cover,
			"author":       it.AuthorName,
			"author_mid":   it.AuthorMid,
			"badge":        it.Badge,
			"show_title":   it.ShowTitle,
			"bvid":         it.History.Bvid,
			"duration":     it.Duration,
			"progress":     it.Progress,
			"view_at":      it.ViewAt,
			"url":          histURLFor(it),
			"followed_by":  jar.get("DedeUserID"),
			"published_at": now.Unix(),
		}
		raw, aerr := fbshared.InsertFlatFields(it.Raw, map[string]any{
			"proto_type":  payload["kind"],
			"cycle_count": len(items),
		})
		if aerr != nil {
			log.Printf("bilibili: publish history %s error: %v", key, aerr)
		} else if m, derr := biliMutableMap(raw); derr != nil {
			log.Printf("bilibili: publish history %s error: %v", key, derr)
		} else if err := publish(m); err != nil {
			log.Printf("bilibili: publish history %s error: %v", key, err)
		}
		published++
	}
	if published > 0 {
		log.Printf("bilibili: history round published %d new entries", published)
	} else {
		log.Printf("bilibili: history round no change")
	}
	return published
}
