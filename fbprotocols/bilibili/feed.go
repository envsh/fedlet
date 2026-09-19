package bilibili

// Follow feed (x/polymer/web-dynamic/v1/feed/all). Needs SESSDATA.
// Publishes newly-appeared updates only (dedupe by id_str).

import (
	"bytes"
	"fmt"
	"log"
	"strconv"
	"time"
)

// flexInt64 accepts either a JSON number or a "123" string. Bilibili has been
// migrating numeric fields of web-dynamic feed/all to strings (pub_ts arrives
// as "1789454019", opus stat.like/view are documented strings); decoding a
// string into a plain int64 fails and aborts the whole round. This dual-form
// type keeps the struct tolerant of both shapes.
type flexInt64 int64

// UnmarshalJSON handles number, "number" and null forms.
func (f *flexInt64) UnmarshalJSON(b []byte) error {
	s := bytes.Trim(b, `"`)
	if string(s) == "null" || len(s) == 0 {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(string(s), 10, 64)
	if err != nil {
		return err
	}
	*f = flexInt64(n)
	return nil
}

const feedURL = apiHost + "/x/polymer/web-dynamic/v1/feed/all?type=all&platform=web"

// feedItem is the per-entry shape of feed/all.data.items.
type feedItem struct {
	IDStr   string `json:"id_str"`
	Type    string `json:"type"`
	Modules struct {
		ModuleAuthor struct {
			Name     string    `json:"name"`
			Mid      int64     `json:"mid"`
			PubTime  flexInt64 `json:"pub_ts"`
			TimeUnix flexInt64 `json:"time_unix"`
		} `json:"module_author"`
		ModuleDynamic struct {
			Desc struct {
				Text string `json:"text"`
			} `json:"desc"`
			Major struct {
				Type    string `json:"type"`
				Archive *struct {
					Title string    `json:"title"`
					Bvid  string    `json:"bvid"`
					Aid   flexInt64 `json:"aid"`
					Pic   string    `json:"cover"`
					Desc  string    `json:"desc"`
					Stat  struct {
						View flexInt64 `json:"view"`
						Like flexInt64 `json:"like"`
					} `json:"stat"`
				} `json:"archive"`
				Draw *struct {
					Title string `json:"title"`
					Items []struct {
						Src string `json:"src"`
					} `json:"items"`
				} `json:"draw"`
				Article *struct {
					Title string `json:"title"`
				} `json:"article"`
				Common *struct {
					Title string `json:"title"`
				} `json:"common"`
				UgcSeason *struct {
					Title string `json:"title"`
					Pic   string `json:"cover"`
				} `json:"ugc_season"`
				Pgc *struct {
					Title string `json:"title"`
					Pic   string `json:"cover"`
				} `json:"pgc"`
			} `json:"major"`
		} `json:"module_dynamic"`
	} `json:"modules"`
}

type feedData struct {
	Items   []feedItem `json:"items"`
	Offset  string     `json:"offset"`
	HasMore bool       `json:"has_more"`
}

// feedItemID returns the dedupe key.
func feedItemID(it *feedItem) string {
	if it.IDStr != "" {
		return it.IDStr
	}
	return fmt.Sprintf("feed:%s:%d", it.Type, it.Modules.ModuleAuthor.Mid)
}

// FetchFollowFeed fetches the follow feed page (latest updates only).
func FetchFollowFeed() ([]feedItem, error) {
	ensureWarmup()
	if jar.get("SESSDATA") == "" {
		return nil, ErrNotLoggedIn
	}
	var data feedData
	if err := getJSON(&data, feedURL, true); err != nil {
		return nil, err
	}
	return data.Items, nil
}

// feedText extracts a readable one-line description for a feed item.
func feedText(it *feedItem) string {
	if it.Modules.ModuleDynamic.Major.Archive != nil {
		a := it.Modules.ModuleDynamic.Major.Archive
		if a.Title != "" {
			return a.Title
		}
	}
	if it.Modules.ModuleDynamic.Major.Draw != nil {
		return it.Modules.ModuleDynamic.Major.Draw.Title
	}
	if it.Modules.ModuleDynamic.Major.Article != nil {
		return it.Modules.ModuleDynamic.Major.Article.Title
	}
	if it.Modules.ModuleDynamic.Major.Common != nil {
		return it.Modules.ModuleDynamic.Major.Common.Title
	}
	if it.Modules.ModuleDynamic.Desc.Text != "" {
		return it.Modules.ModuleDynamic.Desc.Text
	}
	return fmt.Sprintf("[%s]", it.Type)
}

// feedURLFor returns a deep link for a feed entry.
func feedURLFor(it *feedItem) string {
	if a := it.Modules.ModuleDynamic.Major.Archive; a != nil {
		if a.Bvid != "" {
			return "https://www.bilibili.com/video/" + a.Bvid
		}
		if a.Aid != 0 {
			return fmt.Sprintf("https://www.bilibili.com/video/av%d", a.Aid)
		}
	}
	if it.Modules.ModuleAuthor.Mid != 0 {
		return fmt.Sprintf("https://space.bilibili.com/%d/dynamic", it.Modules.ModuleAuthor.Mid)
	}
	return ""
}

// feedImage returns the entry visual: video/season cover, else the first draw
// image. Covers are published verbatim; bilibili serves them over http, which
// the hdslb CDN transparently upgrades to https.
func feedImage(it *feedItem) string {
	m := it.Modules.ModuleDynamic.Major
	if a := m.Archive; a != nil && a.Pic != "" {
		return a.Pic
	}
	if s := m.UgcSeason; s != nil && s.Pic != "" {
		return s.Pic
	}
	if p := m.Pgc; p != nil && p.Pic != "" {
		return p.Pic
	}
	if d := m.Draw; d != nil && len(d.Items) > 0 {
		return d.Items[0].Src
	}
	return ""
}

// feedImages returns all attached images (draw items); nil otherwise.
func feedImages(it *feedItem) []string {
	d := it.Modules.ModuleDynamic.Major.Draw
	if d == nil {
		return nil
	}
	out := make([]string, 0, len(d.Items))
	for _, im := range d.Items {
		if im.Src != "" {
			out = append(out, im.Src)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// feedRound fetches + publishes new follow updates. Returns the number
// published (0 on first sync: the existing history is seeded silently).
func feedRound(state *biliState) int {
	items, err := FetchFollowFeed()
	if err != nil {
		log.Printf("bilibili: follow feed error: %v", err)
		pushError(err)
		return 0
	}
	now := time.Now()
	if len(state.FollowFeed) == 0 {
		for i := range items {
			state.FollowFeed[feedItemID(&items[i])] = now.Unix()
		}
		log.Printf("bilibili: follow feed first sync, seeded %d existing items", len(items))
		return 0
	}
	published := 0
	for i := range items {
		it := &items[i]
		key := feedItemID(it)
		if _, seen := state.FollowFeed[key]; seen {
			continue
		}
		state.FollowFeed[key] = now.Unix()
		text := feedText(it)
		log.Printf("bilibili: feed %s by %s %s", it.Type, it.Modules.ModuleAuthor.Name, truncate(text, 60))
		payload := map[string]any{
			"kind":         "follow_feed",
			"id":           it.IDStr,
			"type":         it.Type,
			"author":       it.Modules.ModuleAuthor.Name,
			"author_mid":   it.Modules.ModuleAuthor.Mid,
			"pub_ts":       it.Modules.ModuleAuthor.PubTime,
			"text":         text,
			"url":          feedURLFor(it),
			"image":        feedImage(it),
			"images":       feedImages(it),
			"followed_by":  jar.get("DedeUserID"),
			"published_at": now.Unix(),
		}
		if err := publish(payload); err != nil {
			log.Printf("bilibili: publish feed %s error: %v", key, err)
		}
		published++
	}
	return published
}
