package xhs

// Hot board (热搜榜), the real xhs trending keyword list.
//
// xhs exposes no signed hot-board API on its web endpoints: probing 2026-09
// showed edith has no hsearch/trending route (HTTP 404 for every variant) and
// the www API gateway rejects all request shapes with "create invoker failed"
// even with a verified logged-in session, while the /hotsearch page is dead
// (302 -> /404). The board is therefore pulled from the uapis.cn aggregator,
// which is free and keyless and returns the actual xhs 热搜排行榜 (rank,
// hot_value like "947.5w", trend tag, and a search_result jump URL) refreshed
// every 5 minutes.
//
// Publish semantics follow the protocol rule: each list item is forwarded
// verbatim plus flat proto_type/cycle_count; only the keyword is parsed, as the
// dedupe key.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	// hotBoardSource identifies the anonymous aggregator backing the board.
	hotBoardSource = "uapis"

	// uapisHotboardURL is the uapis.cn hot-board aggregate for xiaohongshu.
	uapisHotboardURL = "https://uapis.cn/api/v1/misc/hotboard?type=xiaohongshu"
)

// HotBoardItem is one keyword entry of the hot search board as published by
// the aggregator. Raw keeps the exact list item so the published body is the
// source verbatim.
type HotBoardItem struct {
	Keyword  string         `json:"keyword"`
	Rank     int            `json:"rank"`
	HotValue string         `json:"hot_value"`
	Type     string         `json:"type"`
	URL      string         `json:"url"`
	Cover    string         `json:"cover"`
	Raw      map[string]any `json:"-"`
}

// HotBoardResp is the response envelope of the hot board.
type HotBoardResp struct {
	UpdateTime string         `json:"update_time"`
	Items      []HotBoardItem `json:"list"`
}

// FetchHotBoard pulls the current xhs hot search board from the anonymous
// aggregator (plain HTTP; no session, no signature).
func FetchHotBoard() (*HotBoardResp, error) {
	out, err := fetchRawJSON(uapisHotboardURL)
	if err != nil {
		return nil, err
	}
	return parseHotBoardItems(out)
}

// parseHotBoardItems decodes the aggregator envelope into HotBoardItems.
func parseHotBoardItems(out map[string]any) (*HotBoardResp, error) {
	itemsRaw, _ := out["list"].([]any)
	resp := &HotBoardResp{Items: make([]HotBoardItem, 0, len(itemsRaw))}
	if s, ok := out["update_time"].(string); ok {
		resp.UpdateTime = s
	}
	for i, it := range itemsRaw {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		item := HotBoardItem{
			Keyword:  strField(m, "title"),
			HotValue: strField(m, "hot_value"),
			URL:      strField(m, "url"),
			Cover:    strField(m, "cover"),
			Rank:     i + 1,
			Raw:      m,
		}
		if idx, ok := m["index"]; ok {
			if n, ok := numAsInt(idx); ok {
				item.Rank = int(n)
			}
		}
		if ex, ok := m["extra"].(map[string]any); ok {
			item.Type = strField(ex, "type")
		}
		if item.Type == "" {
			item.Type = strField(m, "type")
		}
		resp.Items = append(resp.Items, item)
	}
	if len(resp.Items) == 0 {
		return nil, errors.New("xhs: hot board returned no items")
	}
	return resp, nil
}

// fetchRawJSON GETs a JSON endpoint with the shared plain client and decodes
// it into a map. Used for the anonymous aggregator (no signing).
func fetchRawJSON(url string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", xhsUA)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xhs: hot board get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xhs: hot board http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("xhs: hot board read: %w", err)
	}
	var out map[string]any
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("xhs: hot board decode: %w", err)
	}
	return out, nil
}

// HotBoardTitle is the keyword itself (the search term, not a note title).
func HotBoardTitle(it *HotBoardItem) string {
	if it == nil {
		return ""
	}
	return it.Keyword
}

// HotBoardDetail summarizes the heat value and trend tag.
func HotBoardDetail(it *HotBoardItem) string {
	if it == nil {
		return ""
	}
	s := "🔥 " + it.HotValue
	if it.Type != "" {
		s += " · " + it.Type
	}
	return s
}

// HotBoardLink is the xhs search page for the keyword (type=51 hot search).
func HotBoardLink(it *HotBoardItem) string {
	if it == nil {
		return ""
	}
	return it.URL
}

// strField is a tolerant string reader for generic JSON values.
func strField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}