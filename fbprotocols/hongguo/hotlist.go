package hongguo

// hotlist.go — fetch + parser for the 红果短剧 ranked board.
//
// Source: https://hongguoduanju.com/category/real-drama is a public
// server-rendered page that embeds its initial data as
//
//	_ROUTER_DATA = {"loaderData":{"category_$":{"recommendList":[…]}},…}
//
// The loader embed has no "window." prefix (measured live 2026-09-22), and the
// same data may alternatively be inlined as a <script id="__MODERN_ROUTER_DATA__">
// JSON node. Both forms are accepted; only when both are absent do we fail.
// We walk loaderData for a map carrying a recommendList[]. Each entry is
// published verbatim (Raw source map preserved) with a flat
// series_id/title/url/tags/episode_cnt envelope.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	hotBoardScheme   = "https"
	hotBoardHost     = "hongguoduanju.com"
	hotBoardCategory = "real-drama"
	hotBoardTimeout  = 20 * time.Second
)

// routerMarker is the primary embed literal the live page emits
// ("<script …>_ROUTER_DATA = {…}</script>", measured without a window. prefix).
const routerMarker = "_ROUTER_DATA = "

// routerNode is the secondary embed form: a <script id="__MODERN_ROUTER_DATA__">
// whose textContent is the router JSON (the site's initRouterData parses it
// with JSON.parse(e.textContent)).
const routerNode = `id="__MODERN_ROUTER_DATA__"`

// HotBoardItem is the published shape of one ranked drama. Raw keeps the exact
// source map so forward-as-is consumers see the original card.
type HotBoardItem struct {
	Rank       int            `json:"rank,omitempty"`
	SeriesID   string         `json:"series_id"`
	Title      string         `json:"title"`
	URL        string         `json:"url,omitempty"`
	Cover      string         `json:"cover,omitempty"`
	Tags       []string       `json:"tags,omitempty"`
	EpisodeCnt int            `json:"episode_cnt,omitempty"`
	Raw        map[string]any `json:"-"`
}

// HotBoardResp is the wire envelope list.
type HotBoardResp struct {
	Items []HotBoardItem `json:"list"`
}

// FetchHotBoard fetches the ranked board and returns the parsed cards.
func FetchHotBoard() ([]HotBoardItem, error) {
	body, err := fetchHTML()
	if err != nil {
		return nil, err
	}
	router, err := extractRouterData(body)
	if err != nil {
		return nil, err
	}
	resp, err := parseHotBoardItems(router)
	if err != nil {
		return nil, err
	}
	return resp.Items, nil
}

func fetchHTML() ([]byte, error) {
	addr := hotBoardScheme + "://" + hotBoardHost + "/category/" + hotBoardCategory
	ctx, cancel := context.WithTimeout(context.Background(), hotBoardTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, nil)
	if err != nil {
		return nil, fmt.Errorf("hongguo: build request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hongguo: get %s: %w", addr, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hongguo: %s: status %d", addr, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("hongguo: read body: %w", err)
	}
	return body, nil
}

// extractRouterData cuts the router JSON out of the HTML. Primary form is the
// inline "_ROUTER_DATA = " embed; the page's own JS also mentions the symbol,
// so we walk every occurrence and accept the first that decodes as an object.
// Secondary form (the same data as a __MODERN_ROUTER_DATA__ JSON node) is a
// fallback. Only when both are missing do we error — never best-effort.
func extractRouterData(body []byte) (map[string]any, error) {
	rest := body
	for {
		i := bytes.Index(rest, []byte(routerMarker))
		if i < 0 {
			break
		}
		seg := rest[i+len(routerMarker):]
		rest = seg
		if j := bytes.Index(seg, []byte("</script>")); j >= 0 {
			seg = seg[:j]
		}
		if router, err := decodeRouterJSON(seg); err == nil {
			return router, nil
		}
	}

	// Fallback: the __MODERN_ROUTER_DATA__ JSON node.
	if i := bytes.Index(body, []byte(routerNode)); i >= 0 {
		if open := bytes.LastIndex(body[:i], []byte("<script")); open >= 0 {
			// marker id sits inside the tag's attribute region; the tag's
			// closing ">" comes AFTER it.
			if gt := bytes.Index(body[i:], []byte(">")); gt >= 0 {
				seg := body[i+gt+1:]
				if j := bytes.Index(seg, []byte("</script>")); j >= 0 {
					if router, err := decodeRouterJSON(bytes.TrimSpace(seg[:j])); err == nil {
						return router, nil
					}
				}
			}
		}
	}

	return nil, errors.New("hongguo: _ROUTER_DATA embed missing")
}

// decodeRouterJSON trims a trailing ";" and decodes the router object with
// json.Number so the 19-digit series_id round-trips exactly.
func decodeRouterJSON(raw []byte) (map[string]any, error) {
	raw = bytes.TrimSpace(raw)
	raw = bytes.TrimSpace(bytes.TrimRight(raw, ";"))
	if len(raw) == 0 {
		return nil, errors.New("hongguo: _ROUTER_DATA embed empty")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var router map[string]any
	if err := dec.Decode(&router); err != nil {
		return nil, fmt.Errorf("hongguo: decode _ROUTER_DATA: %w", err)
	}
	if router == nil {
		return nil, errors.New("hongguo: _ROUTER_DATA not an object")
	}
	return router, nil
}

// parseHotBoardItems walks the whole router map (loaderData nesting depth and
// category key may change) and returns the first map that carries a
// recommendList[]. An empty recommendList is an error: a live board with zero
// items is treated as a failed/refreshed layout.
func parseHotBoardItems(router map[string]any) (*HotBoardResp, error) {
	var list []any
	var found bool

	var find func(any) bool
	find = func(v any) bool {
		switch t := v.(type) {
		case map[string]any:
			if raw, ok := t["recommendList"]; ok {
				if arr, ok := raw.([]any); ok {
					list, found = arr, true
					return true
				}
			}
			for _, child := range t {
				if find(child) {
					return true
				}
			}
		case []any:
			for _, child := range t {
				if find(child) {
					return true
				}
			}
		}
		return false
	}
	find(router)

	if !found {
		return nil, errors.New("hongguo: recommendList not found in _ROUTER_DATA")
	}
	if len(list) == 0 {
		return nil, errors.New("hongguo: recommendList is empty")
	}

	items := make([]HotBoardItem, 0, len(list))
	for i, rawCard := range list {
		card, ok := rawCard.(map[string]any)
		if !ok {
			continue
		}
		item := HotBoardItem{
			Rank: i + 1,
			Raw:  card,
		}
		item.SeriesID = toIDString(card["series_id"])
		item.Title = toString(card["series_name"])
		if item.SeriesID != "" {
			item.URL = hotBoardScheme + "://" + hotBoardHost + "/detail?series_id=" + item.SeriesID
		}
		item.Cover = toString(card["series_cover"])
		item.Tags = toStrings(card["tags"])
		item.EpisodeCnt = toInt(card["episode_cnt"])
		items = append(items, item)
	}
	return &HotBoardResp{Items: items}, nil
}

func toIDString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return strconv.FormatInt(int64(toInt(v)), 10)
	}
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func toStrings(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s := toString(e); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func toInt(v any) int {
	switch t := v.(type) {
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n)
		}
	case float64:
		return int(t)
	case string:
		if n, err := strconv.Atoi(t); err == nil {
			return n
		}
	case bool:
		if t {
			return 1
		}
	}
	return 0
}
