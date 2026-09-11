package zhihu

// Zhihu Daily ("知乎日报"): the public daily.zhihu.com bulletin, independent of
// the www.zhihu.com session (verified 2026-09: no cookie, no signature, no login;
// every endpoint answers 200 anonymously). The old news-at.zhihu.com API domain
// is dead (RSSHub #17325, 2024-10); the live surface is:
//
//	GET https://daily.zhihu.com/api/4/news/latest  -> json {date, stories[], top_stories[]}
//	GET https://daily.zhihu.com/api/4/story/{id}    -> json {title, body(HTML), image, images}
//
// Unlike the signed feeds this host takes no auth and none of our
// session/risk-control machinery is reused: it goes straight through hc with a
// browser UA + Referer. Failures are surfaced as status errors only.

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
)

const (
	dailyLatestURL = "https://daily.zhihu.com/api/4/news/latest"
	dailyReferer   = "https://daily.zhihu.com/"
)

// DailyStory is one entry of the daily bulletin list.
type DailyStory struct {
	ID       int64    `json:"id"`
	Title    string   `json:"title"`
	URL      string   `json:"url"`
	Hint     string   `json:"hint"`
	Images   []string `json:"images"`
	ImageHue string   `json:"image_hue"`
	Type     int      `json:"type"`
	GaPrefix string   `json:"ga_prefix"`
}

// DailyLatest is the /news/latest envelope.
type DailyLatest struct {
	Date       string       `json:"date"`
	Stories    []DailyStory `json:"stories"`
	TopStories []DailyStory `json:"top_stories"`
}

// DailyStoryDetail is the /story/{id} article payload.
type DailyStoryDetail struct {
	ID       int64    `json:"id"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Image    string   `json:"image"`
	Images   []string `json:"images"`
	ShareURL string   `json:"share_url"`
	GaPrefix string   `json:"ga_prefix"`
}

// FetchDailyLatest fetches the current bulletin. No authentication.
func FetchDailyLatest() (*DailyLatest, error) {
	var r DailyLatest
	if err := getDailyJSON(&r, dailyLatestURL); err != nil {
		return nil, err
	}
	return &r, nil
}

// FetchDailyStory fetches one article (raw HTML body). No authentication.
func FetchDailyStory(id int64) (*DailyStoryDetail, error) {
	var r DailyStoryDetail
	u := fmt.Sprintf("https://daily.zhihu.com/api/4/story/%d", id)
	if err := getDailyJSON(&r, u); err != nil {
		return nil, err
	}
	return &r, nil
}

// getDailyJSON is the anonymous GET for daily.zhihu.com: browser UA, JSON
// Accept and the daily page as Referer; no cookie, no signature, no rate gate.
func getDailyJSON(data any, u string) error {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("zhihu: daily request: %w", err)
	}
	req.Header.Set("User-Agent", zhihuWebUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", dailyReferer)
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("zhihu: daily: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("zhihu: daily http %d %s", resp.StatusCode, u)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("zhihu: daily read %s: %w", u, err)
	}
	if err := json.Unmarshal(body, data); err != nil {
		return fmt.Errorf("zhihu: daily parse %s: %w", u, err)
	}
	return nil
}

// dailySummaryLen caps the description (rune-aware, via truncate).
const dailySummaryLen = 250

// stripDailyBody converts the story body HTML into a plain-text summary.
func stripDailyBody(body string) string {
	s := htmlTagRE.ReplaceAllString(body, " ")
	s = html.UnescapeString(s)
	s = strings.Join(strings.Fields(s), " ")
	return truncate(s, dailySummaryLen)
}
