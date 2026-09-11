package toutiao

// Toutiao hot board ("热榜"), the trending panel shown on www.toutiao.com.
//
//	GET https://www.toutiao.com/hot-event/hot-board/?origin=toutiao_pc
//
// Officially public: no login, no cookies, no signature (verified live
// 2026-09-10 from an anonymous client; UA + Referer suffice). The live board
// is cached server-side for ~5-10 minutes, so a 600s poll matches its cadence.
// Items are tracked by ClusterIdStr so a stable board only publishes entries
// that newly appeared.

import (
	"encoding/json"
	"fmt"
)

// hotlistURL is the PC web hot-board endpoint (origin=toutiao_pc).
const hotlistURL = "https://www.toutiao.com/hot-event/hot-board/?origin=toutiao_pc"

// HotlistResp is the response envelope of the hot board.
type HotlistResp struct {
	Status string        `json:"status"`
	Data   []HotlistItem `json:"data"`
}

// HotlistItem is one entry of the hot board. Field names match the real
// response (capitalized); HotValue is kept raw because it appears both as a
// JSON string ("13830954") and occasionally as a number.
type HotlistItem struct {
	ClusterID        int64           `json:"ClusterId"`
	ClusterIDStr     string          `json:"ClusterIdStr"`
	Title            string          `json:"Title"`
	QueryWord        string          `json:"QueryWord"`
	Label            string          `json:"Label"`
	LabelDesc        string          `json:"LabelDesc"`
	InterestCategory []string        `json:"InterestCategory"`
	HotValue         json.RawMessage `json:"HotValue"`
	Url              string          `json:"Url"`
	Image            map[string]any  `json:"Image"`
}

// FetchHotlist fetches the current hot board anonymously.
func FetchHotlist() (*HotlistResp, error) {
	body, err := fetchJSON(hotlistURL)
	if err != nil {
		return nil, fmt.Errorf("toutiao: hotlist: %w", err)
	}
	r, err := parseHotlist(body)
	if err != nil {
		return nil, fmt.Errorf("toutiao: hotlist: %w", err)
	}
	return r, nil
}

// Key returns the dedupe key of the entry.
func (it *HotlistItem) Key() string {
	if it == nil {
		return ""
	}
	if it.ClusterIDStr != "" {
		return it.ClusterIDStr
	}
	if it.ClusterID != 0 {
		return fmt.Sprintf("%d", it.ClusterID)
	}
	return ""
}

// HotlistTitle returns the display title, falling back to the query word.
func HotlistTitle(it *HotlistItem) string {
	if it == nil {
		return ""
	}
	if it.Title != "" {
		return it.Title
	}
	return it.QueryWord
}

// HotlistHot parses the (string-or-number) HotValue and renders it compactly.
func HotlistHot(it *HotlistItem) string {
	if it == nil || len(it.HotValue) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(it.HotValue, &s); err == nil && s != "" {
		return formatHeat(parseInt(s))
	}
	var n int64
	if err := json.Unmarshal(it.HotValue, &n); err == nil {
		return formatHeat(n)
	}
	return ""
}

// HotlistDetail returns the trending context (label description or interest
// categories) plus the heat value, used as the published "detail" field.
func HotlistDetail(it *HotlistItem) string {
	if it == nil {
		return ""
	}
	parts := []string{}
	if it.LabelDesc != "" {
		parts = append(parts, it.LabelDesc)
	} else if len(it.InterestCategory) > 0 {
		parts = append(parts, it.InterestCategory...)
	}
	if h := HotlistHot(it); h != "" {
		parts = append(parts, "热度 "+h)
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " / "
		}
		out += p
	}
	return out
}

// HotlistLink returns the clean topic permalink. The raw Url carries tracking
// params (log_pb); the trending/{ClusterIdStr} form is what browsers open.
func HotlistLink(it *HotlistItem) string {
	if it == nil {
		return ""
	}
	if id := it.ClusterIDStr; id != "" {
		return "https://www.toutiao.com/trending/" + id + "/"
	}
	if u := cleanHotboardURL(it.Url); u != "" {
		return u
	}
	if it.ClusterID != 0 {
		return fmt.Sprintf("https://www.toutiao.com/trending/%d/", it.ClusterID)
	}
	return ""
}

// cleanHotboardURL strips the tracking query from the raw hot-board Url so the
// topic permalink stays clean when no ClusterIdStr is available.
func cleanHotboardURL(raw string) string {
	i := indexAny(raw, "?#")
	if i < 0 {
		return raw
	}
	return raw[:i]
}

func indexAny(s string, chars string) int {
	for i := 0; i < len(s); i++ {
		if containsByte(chars, s[i]) {
			return i
		}
	}
	return -1
}

func containsByte(s string, b byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return true
		}
	}
	return false
}
