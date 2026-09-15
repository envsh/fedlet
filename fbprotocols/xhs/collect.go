package xhs

// Own favorites (collections) — the notes the account has saved into its
// 收藏 (collect) shelf. Needs a real login (guest sessions have no shelf).
//
// Endpoint (ReaJason/xhs parity, 待现场核实):
//
//	GET /api/sns/web/v2/note/collect/page?user_id=&num=&cursor=
//
// Publishes newly-collected notes only (dedupe by note_id); the first
// successful sync seeds the current shelf silently.

import (
	"fmt"
	"strings"
	"time"
)

const collectURL = "/api/sns/web/v2/note/collect/page"

const (
	collectPageSize = 30
	collectPageCap  = 10
)

// CollectNote is one flat collected-note snapshot.
type CollectNote struct {
	NoteID         string
	Title          string
	Type           string
	Cover          string
	Author         string
	AuthorID       string
	LikedCount     int64
	CollectedCount int64
}

// collectPageResp is the parsed {cursor,has_more,notes} envelope.
type collectPageResp struct {
	Cursor  string
	HasMore bool
	Notes   []CollectNote
}

// collectPage fetches one page of the collect shelf (signed, own user_id).
func collectPage(userID, cursor string, num int) (collectPageResp, error) {
	var resp collectPageResp
	out, status, err := client().exec(signHeadersOptions{
		method: "GET",
		uri:    collectURL,
		params: []kvParam{
			{key: "user_id", val: userID},
			{key: "num", val: itoaInt(num)},
			{key: "cursor", val: cursor},
		},
		userID: userID,
		ts:     float64AtNow(),
	})
	if err != nil {
		return resp, err
	}
	data, status, err := decodeXHSPayload(out, status)
	if err != nil {
		return resp, err
	}
	if c, ok := data["cursor"].(string); ok {
		resp.Cursor = c
	}
	if hasMore, ok := data["has_more"].(bool); ok {
		resp.HasMore = hasMore
	}
	if notes, ok := data["notes"].([]any); ok {
		for _, raw := range notes {
			if m, ok := raw.(map[string]any); ok {
				resp.Notes = append(resp.Notes, parseCollectNote(m))
			}
		}
	}
	return resp, nil
}

// parseCollectNote flattens one collect-shelf note object.
func parseCollectNote(m map[string]any) CollectNote {
	n := CollectNote{
		NoteID: xstr(m, "note_id"),
		Title:  xstr(m, "display_title"),
		Type:   xstr(m, "type"),
		Cover:  xstr(m, "cover"),
	}
	if n.Title == "" {
		n.Title = xstr(m, "title")
	}
	if u, ok := m["user"].(map[string]any); ok {
		n.Author = xstr(u, "nickname")
		n.AuthorID = xstr(u, "user_id")
	}
	if ii, ok := m["interact_info"].(map[string]any); ok {
		n.LikedCount = xint(ii, "liked_count")
		n.CollectedCount = xint(ii, "collected_count")
	}
	return n
}

// collectKey returns the dedupe key.
func collectKey(n *CollectNote) string {
	return n.NoteID
}

// collectLink returns the note's web URL.
func collectLink(n *CollectNote) string {
	if n.NoteID == "" {
		return ""
	}
	return "https://www.xiaohongshu.com/explore/" + n.NoteID
}

// FetchMyCollect fetches the account's collect shelf (recent first). A guest
// or missing session fails fast with ErrNotLoggedIn.
func FetchMyCollect() ([]CollectNote, error) {
	userID := AuthUserID()
	if userID == "" {
		// The session verify records the id; without it the shelf is not ours.
		if err := verifySession(); err != nil {
			return nil, err
		}
		userID = AuthUserID()
		if userID == "" {
			return nil, ErrNotLoggedIn
		}
	}
	var out []CollectNote
	cursor := ""
	for page := 0; page < collectPageCap; page++ {
		resp, err := collectPage(userID, cursor, collectPageSize)
		if err != nil {
			return nil, err
		}
		out = append(out, resp.Notes...)
		if !resp.HasMore || len(resp.Notes) == 0 || resp.Cursor == "" || resp.Cursor == cursor {
			break
		}
		cursor = resp.Cursor
	}
	return out, nil
}

// collectRound fetches + publishes newly-collected notes. Returns the number
// published (0 on first sync: the existing shelf is seeded silently).
func collectRound(state *xhsState) {
	notes, err := FetchMyCollect()
	if err != nil {
		logPrefix("collect error: %v", err)
		handleRoundErr(state, err)
		return
	}
	now := time.Now()
	if len(state.Collect) == 0 {
		for i := range notes {
			state.Collect[collectKey(&notes[i])] = now.Unix()
		}
		logPrefix("collect first sync, seeded %d existing notes", len(notes))
		return
	}
	published := 0
	for i := range notes {
		it := &notes[i]
		key := collectKey(it)
		if key == "" {
			continue
		}
		if _, seen := state.Collect[key]; seen {
			continue
		}
		state.Collect[key] = now.Unix()
		logPrefix("collect %s %s", it.NoteID, truncate(it.Title, 60))
		payload := map[string]any{
			"kind":            "xhs_collect",
			"note_id":         it.NoteID,
			"title":           it.Title,
			"type":            it.Type,
			"cover":           it.Cover,
			"author":          it.Author,
			"author_id":       it.AuthorID,
			"liked_count":     it.LikedCount,
			"collected_count": it.CollectedCount,
			"url":             collectLink(it),
			"published_at":    now.Unix(),
		}
		if err := publish(payload); err != nil {
			logPrefix("publish collect %s error: %v", key, err)
		}
		published++
	}
	if published > 0 {
		logPrefix("collect round published %d new notes", published)
	} else {
		logPrefix("collect round no change")
	}
}

func xstr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

func xint(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case string:
		var n int64
		fmt.Sscanf(v, "%d", &n)
		return n
	}
	return 0
}

func itoaInt(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
