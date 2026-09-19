package zhihu

// Own browse history (unify-consumption/read_history). Needs the main z_c0
// session. Publishes newly-read entries only (dedupe by content_type+token).
//
// Endpoint (same-source as the zhihu web API, 待现场核实):
//
//	GET /api/v4/unify-consumption/read_history?offset=&limit=
//	GET /api/v4/read_history/total -> {disabled,count,has_batch_report}
//
// Each entry's `data.extra.content_token` is the stable per-{type,content} id;
// re-reading the same content renews read_time but keeps the same token.

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

const readHistoryURL = "https://www.zhihu.com/api/v4/unify-consumption/read_history?offset=%d&limit=%d"

const (
	readHistoryPageSize = 20
	readHistoryPageCap  = 10
	readHistoryReferer  = "https://www.zhihu.com/"
)

// HistoryEntry is one flat browse-history item.
type HistoryEntry struct {
	CardType      string
	ContentToken  string
	ContentType   string
	QuestionToken string
	ReadTime      int64
	Author        string
	Summary       string
	Cover         string
	URL           string
	Raw           json.RawMessage
}

// parseHistoryEntry decodes one unify-consumption record into the flat view.
func parseHistoryEntry(raw json.RawMessage) HistoryEntry {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return HistoryEntry{}
	}
	var e HistoryEntry
	e.Raw = append(json.RawMessage(nil), raw...)
	e.CardType, _ = m["card_type"].(string)
	// The payload nests under data.data (the read record) -> data.extra + data.content.
	rec, ok := m["data"].(map[string]any)
	if !ok {
		return e
	}
	if d, ok := rec["data"].(map[string]any); ok {
		rec = d
	}
	if x, ok := rec["extra"].(map[string]any); ok {
		e.ContentToken, _ = x["content_token"].(string)
		e.ContentType, _ = x["content_type"].(string)
		e.QuestionToken, _ = x["question_token"].(string)
		e.ReadTime = intField(x, "read_time")
	}
	if c, ok := rec["content"].(map[string]any); ok {
		e.Author, _ = c["author_name"].(string)
		e.Summary, _ = c["summary"].(string)
		e.Cover, _ = c["cover_image"].(string)
	}
	if a, ok := rec["action"].(map[string]any); ok {
		e.URL, _ = a["url"].(string)
	}
	if e.URL == "" {
		e.URL = historyLink(e.ContentType, e.ContentToken, e.QuestionToken)
	}
	return e
}

// historyLink builds a deep link when the API omits it.
func historyLink(contentType, token, questionToken string) string {
	switch contentType {
	case "answer", "question":
		if questionToken != "" && token != "" {
			return fmt.Sprintf("https://www.zhihu.com/question/%s/answer/%s", questionToken, token)
		}
	case "article":
		if token != "" {
			return "https://zhuanlan.zhihu.com/p/" + token
		}
	}
	return ""
}

// historyItemKey returns the dedupe key.
func historyItemKey(e *HistoryEntry) string {
	return e.ContentType + ":" + e.ContentToken
}

// FetchHistoryPage fetches one page of the browse history.
func fetchHistoryPage(offset int) ([]HistoryEntry, int, bool, error) {
	u := fmt.Sprintf(readHistoryURL, offset, readHistoryPageSize)
	var r struct {
		Data   []json.RawMessage `json:"data"`
		Paging struct {
			IsEnd  bool `json:"is_end"`
			Totals int  `json:"totals"`
		} `json:"paging"`
	}
	if err := getJSONRef(&r, u, true, readHistoryReferer); err != nil {
		return nil, 0, false, err
	}
	out := make([]HistoryEntry, 0, len(r.Data))
	for i := range r.Data {
		out = append(out, parseHistoryEntry(r.Data[i]))
	}
	return out, r.Paging.Totals, r.Paging.IsEnd, nil
}

// FetchMyHistory fetches the account's recent browse history (recent first),
// along with the reported total (when present).
func FetchMyHistory() ([]HistoryEntry, int, error) {
	var out []HistoryEntry
	total := -1
	offset := 0
	for page := 0; page < readHistoryPageCap; page++ {
		entries, pageTotal, isEnd, err := fetchHistoryPage(offset)
		if err != nil {
			return nil, total, err
		}
		out = append(out, entries...)
		if page == 0 && pageTotal > 0 {
			total = pageTotal
		}
		offset += len(entries)
		if isEnd || len(entries) == 0 {
			break
		}
	}
	return out, total, nil
}

// historyRound fetches + publishes newly-read entries. Returns the number
// published (0 on first sync: the existing history is seeded silently).
func historyRound(state *zhihuState) {
	entries, total, err := FetchMyHistory()
	if err != nil {
		log.Printf("zhihu: history error: %v", err)
		handleRoundErr(state, err)
		return
	}
	now := time.Now()
	if len(state.History) == 0 {
		for i := range entries {
			state.History[historyItemKey(&entries[i])] = now.Unix()
		}
		log.Printf("zhihu: history first sync, seeded %d existing entries", len(entries))
		return
	}
	published := 0
	for i := range entries {
		e := &entries[i]
		key := historyItemKey(e)
		if key == ":" {
			continue
		}
		if _, seen := state.History[key]; seen {
			continue
		}
		state.History[key] = now.Unix()
		log.Printf("zhihu: history %s %s @%d", e.ContentType, e.ContentToken, e.ReadTime)
		payload := map[string]any{
			"kind":           "zhihu_history",
			"content_token":  e.ContentToken,
			"content_type":   e.ContentType,
			"question_token": e.QuestionToken,
			"read_time":      e.ReadTime,
			"author":         e.Author,
			"summary":        e.Summary,
			"cover":          e.Cover,
			"url":            e.URL,
			"total":          total,
			"published_at":   now.Unix(),
		}
		raw, aerr := fbshared.InsertFlatFields(e.Raw, map[string]any{
			"proto_type":  payload["kind"],
			"cycle_count": len(entries),
		})
		if aerr != nil {
			log.Printf("zhihu: publish history %s error: %v", key, aerr)
		} else if err := publish(raw); err != nil {
			log.Printf("zhihu: publish history %s error: %v", key, err)
		}
		published++
	}
	if published > 0 {
		log.Printf("zhihu: history round published %d new entries", published)
	} else {
		log.Printf("zhihu: history round no change")
	}
}
