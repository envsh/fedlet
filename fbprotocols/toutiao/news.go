package toutiao

// Real-time news feed ("资讯流"), the non-hot-list article list of the PC web
// home page.
//
//	GET https://www.toutiao.com/api/pc/feed/?category=__all__&max_behot_time=0&count=20
//
// Anonymous access works (verified live 2026-09-10 with only a UA + Referer;
// response "message: success", newest-first by behot_time). Pagination: the
// response carries "next.max_behot_time" and requesting that value yields the
// preceding (older) batch. Feed-style articles are tracked by group_id so only
// newly-appeared items are published.
//
// 待实测: several 2026 reverse-engineering write-ups describe a_bogus/msToken
// signature requirements and tt_webid cookies; the plain endpoint currently
// answers anonymously but may tighten without notice.

import (
	"fmt"
	"net/url"
)

// newsURL is the PC feed endpoint; category=__all__ is the full home stream.
const newsURL = "https://www.toutiao.com/api/pc/feed/"

// newsCount is the per-page item count used by the web client (max ~20).
const newsCount = 20

// NewsResp is the response envelope of the feed endpoint.
type NewsResp struct {
	Message string     `json:"message"`
	HasMore bool       `json:"has_more"`
	Next    NewsNext   `json:"next"`
	Data    []NewsItem `json:"data"`
}

// NewsNext carries the pagination watermark of a feed response.
type NewsNext struct {
	MaxBehotTime int64 `json:"max_behot_time"`
}

// NewsItem is one feed article, flattened from the web payload.
type NewsItem struct {
	Title         string `json:"title"`
	Source        string `json:"source"`
	ChineseTag    string `json:"chinese_tag"`
	GroupID       string `json:"group_id"`
	ItemID        string `json:"item_id"`
	BehotTime     int64  `json:"behot_time"`
	HasVideo      bool   `json:"has_video"`
	CommentsCount int64  `json:"comments_count"`
	ImageURL      string `json:"image_url"`
	IsFeedAd      bool   `json:"is_feed_ad"`
}

// FetchNews fetches page 1 of the feed (newest-first) starting from the given
// behot watermark (0 = the very newest batch).
func FetchNews(maxBehot int64) (*NewsResp, error) {
	q := url.Values{}
	q.Set("category", "__all__")
	q.Set("count", fmt.Sprintf("%d", newsCount))
	q.Set("max_behot_time", fmt.Sprintf("%d", maxBehot))
	body, err := fetchJSON(newsURL + "?" + q.Encode())
	if err != nil {
		return nil, fmt.Errorf("toutiao: news: %w", err)
	}
	r, err := parseNews(body)
	if err != nil {
		return nil, fmt.Errorf("toutiao: news: %w", err)
	}
	return r, nil
}

// Key returns the dedupe key of the article.
func (it *NewsItem) Key() string {
	if it == nil {
		return ""
	}
	if it.GroupID != "" {
		return it.GroupID
	}
	return it.ItemID
}

// IsAd reports whether the entry is a promoted feed item.
func (it *NewsItem) IsAd() bool { return it != nil && it.IsFeedAd }

// NewsLink returns the article detail URL.
func NewsLink(it *NewsItem) string {
	if it == nil || it.Key() == "" {
		return ""
	}
	return "https://www.toutiao.com/article/" + it.Key() + "/"
}

// NewsTag returns the channel tag if any.
func NewsTag(it *NewsItem) string {
	if it == nil {
		return ""
	}
	return it.ChineseTag
}

// NewsComments renders the comment count compactly.
func NewsComments(it *NewsItem) string {
	if it == nil {
		return ""
	}
	return formatHeat(it.CommentsCount)
}

// NewsVideo reports whether the entry is a video item.
func NewsVideo(it *NewsItem) bool { return it != nil && it.HasVideo }
