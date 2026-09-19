package weibo

// Weibo hot boards. All three sections come from ONE public endpoint:
//
//	GET https://weibo.com/ajax/side/hotSearch
//
// Officially public: no login, no cookies, no signature. Verified live
// 2026-09-16 from an anonymous client (browser UA + Referer suffice):
//   - realtime:  hot search main board (微博热搜榜)
//   - hotgov:    trending government topics (政务热搜; single dict + hotgovs list)
//   - band_list: entertainment board (文娱榜, present sporadically)
//
// Everything else on weibo.com (follow feed, topic HTML, m.weibo container)
// requires a login or visitor cookie and is out of scope for this backend.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// hotlistURL is the anonymous hot-board JSON endpoint.
const hotlistURL = "https://weibo.com/ajax/side/hotSearch"

// WeiboResp is the top-level envelope (ok == 1 on success).
type WeiboResp struct {
	OK   int     `json:"ok"`
	Data HotData `json:"data"`
}

// HotData holds the three boards of the same response.
type HotData struct {
	Realtime []HotItem  `json:"realtime"`
	Hotgov   HotGovItem `json:"hotgov"`
	Hotgovs  []HotItem  `json:"hotgovs"`
	BandList []HotItem  `json:"band_list"`
}

// HotItem is one entry of a ranked board (realtime / hotgovs / band_list).
// Num is kept raw because it appears both as a JSON number and occasionally
// as a string.
type HotItem struct {
	Word       string          `json:"word"`
	WordScheme string          `json:"word_scheme"`
	Note       string          `json:"note"`
	Num        json.RawMessage `json:"num"`
	Rank       int64           `json:"rank"`
	Realpos    int64           `json:"realpos"`
	Pos        int64           `json:"pos"`
	LabelName  string          `json:"label_name"`
	IconDesc   string          `json:"icon_desc"`
	TopicFlag  int64           `json:"topic_flag"`
	Raw        json.RawMessage `json:"-"`
}

// UnmarshalJSON keeps the original entry bytes for verbatim forwarding.
func (it *HotItem) UnmarshalJSON(b []byte) error {
	type alias HotItem
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*it = HotItem(a)
	it.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// HotGovItem is the single featured government entry. Its word already
// carries the "#话题#" decoration. IsHot is kept raw because the live
// response sends it as a number (0/1) while docs describe a boolean.
type HotGovItem struct {
	Word     string          `json:"word"`
	Name     string          `json:"name"`
	Note     string          `json:"note"`
	URL      string          `json:"url"`
	Pos      int64           `json:"pos"`
	IconDesc string          `json:"icon_desc"`
	IsHot    json.RawMessage `json:"is_hot"`
	Raw      json.RawMessage `json:"-"`
}

// UnmarshalJSON keeps the original entry bytes for verbatim forwarding.
func (it *HotGovItem) UnmarshalJSON(b []byte) error {
	type alias HotGovItem
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*it = HotGovItem(a)
	it.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// FetchHotlist fetches the current hot boards anonymously.
func FetchHotlist() (*WeiboResp, error) {
	body, err := fetchJSON(hotlistURL)
	if err != nil {
		return nil, fmt.Errorf("weibo: hotlist: %w", err)
	}
	r, err := parseHotlist(body)
	if err != nil {
		return nil, fmt.Errorf("weibo: hotlist: %w", err)
	}
	return r, nil
}

// parseHotlist decodes and validates a hotSearch response body.
func parseHotlist(body []byte) (*WeiboResp, error) {
	var r WeiboResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if r.OK != 1 {
		return nil, fmt.Errorf("weibo hotlist ok=%d", r.OK)
	}
	return &r, nil
}

// Key returns the dedupe key of a board entry (the topic word). Words carry
// real cross-board identity; the round prefixes the section to keep the
// realtime and government boards from colliding.
func (it *HotItem) Key() string {
	if it == nil {
		return ""
	}
	return it.Word
}

// weiboWord strips the "#话题#" decoration to a displayable keyword.
func weiboWord(word string) string {
	return strings.Trim(word, "#")
}

// WeiboTitle returns the display keyword of a board entry.
func WeiboTitle(it *HotItem) string {
	if it == nil {
		return ""
	}
	return weiboWord(it.Word)
}

// WeiboLabel returns the trendy flag ("新"/"热"/"沸"/"爆"/"商"...), falling
// back from label_name to icon_desc when the label is empty.
func WeiboLabel(it *HotItem) string {
	if it == nil {
		return ""
	}
	if it.LabelName != "" {
		return it.LabelName
	}
	return it.IconDesc
}

// WeiboHot parses the (number-or-string) num and renders it compactly (万).
func WeiboHot(it *HotItem) string {
	if it == nil || len(it.Num) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(it.Num, &s); err == nil && s != "" {
		return formatHeat(parseInt(s))
	}
	var n int64
	if err := json.Unmarshal(it.Num, &n); err == nil {
		return formatHeat(n)
	}
	return ""
}

// WeiboLink returns the clean search permalink for a topic word.
func WeiboLink(word string) string {
	w := weiboWord(word)
	if w == "" {
		return ""
	}
	return "https://s.weibo.com/weibo?q=" + url.QueryEscape("#"+w+"#") + "&Refer=top"
}
