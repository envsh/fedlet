package bilibili

// Global hot board (x/web-interface/ranking/v2). Public — no session needed.
// Publishes newly-appeared entries only (dedupe by aid/bvid).
// Publish: each entry forwarded verbatim + flat proto_type/cycle_count (top level).

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

const rankingURL = apiHost + "/x/web-interface/ranking/v2?rid=0&type=all&web_location=333.934"

// rankingItem is the per-entry shape of ranking/v2.data.list.
type rankingItem struct {
	Aid      int64  `json:"aid"`
	Bvid     string `json:"bvid"`
	TypeID   int64  `json:"tid"`
	TypeName string `json:"tname"`
	Rank     int    `json:"rank"`
	Score    int64  `json:"score"`
	Title    string `json:"title"`
	Desc     string `json:"desc"`
	Pic      string `json:"pic"`
	Duration int64  `json:"duration"`
	Owner    struct {
		Mid  int64  `json:"mid"`
		Name string `json:"name"`
	} `json:"owner"`
	Stat struct {
		View     int64 `json:"view"`
		Danmaku  int64 `json:"danmaku"`
		Reply    int64 `json:"reply"`
		Favorite int64 `json:"favorite"`
		Coin     int64 `json:"coin"`
		Share    int64 `json:"share"`
		Like     int64 `json:"like"`
	} `json:"stat"`
	ShortLinkV2 string `json:"short_link_v2"`
	Raw          json.RawMessage `json:"-"`
}

// UnmarshalJSON keeps the original entry bytes for verbatim forwarding.
func (it *rankingItem) UnmarshalJSON(b []byte) error {
	type alias rankingItem
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*it = rankingItem(a)
	it.Raw = append(json.RawMessage(nil), b...)
	return nil
}

type rankingData struct {
	List []rankingItem `json:"list"`
}

// hotItemID returns the dedupe key for a ranking entry.
func hotItemID(it *rankingItem) string {
	if it.Bvid != "" {
		return it.Bvid
	}
	return fmt.Sprintf("av%d", it.Aid)
}

// FetchHotboard fetches the full ranking list (100 entries). No auth, public.
func FetchHotboard() ([]rankingItem, error) {
	ensureWarmup()
	var data rankingData
	if err := getJSON(&data, rankingURL, false); err != nil {
		return nil, err
	}
	return data.List, nil
}

// hotRound fetches + publishes new entries. Returns the number published.
func hotRound(state *biliState) int {
	list, err := FetchHotboard()
	if err != nil {
		log.Printf("bilibili: hotboard error: %v", err)
		pushError(err)
		return 0
	}
	now := time.Now()
	published := 0
	for i := range list {
		it := &list[i]
		key := hotItemID(it)
		if _, seen := state.Hotboard[key]; seen {
			continue
		}
		state.Hotboard[key] = now.Unix()
		log.Printf("bilibili: hotboard #%d %s %s", it.Rank, key, truncate(it.Title, 60))
		payload := map[string]any{
			"kind":         "hotboard",
			"rank":         it.Rank,
			"aid":          it.Aid,
			"bvid":         it.Bvid,
			"title":        it.Title,
			"desc":         it.Desc,
			"type":         it.TypeName,
			"pic":          it.Pic,
			"duration":     it.Duration,
			"owner":        it.Owner.Name,
			"owner_mid":    it.Owner.Mid,
			"view":         it.Stat.View,
			"danmaku":      it.Stat.Danmaku,
			"favorite":     it.Stat.Favorite,
			"coin":         it.Stat.Coin,
			"share":        it.Stat.Share,
			"like":         it.Stat.Like,
			"reply":        it.Stat.Reply,
			"score":        it.Score,
			"url":          "https://www.bilibili.com/video/" + it.Bvid,
			"count":        len(list),
			"published_at": now.Unix(),
		}
		raw, aerr := fbshared.InsertFlatFields(it.Raw, map[string]any{
			"proto_type":  payload["kind"],
			"cycle_count": len(list),
		})
		if aerr != nil {
			log.Printf("bilibili: publish hotboard %s error: %v", key, aerr)
		} else if m, derr := biliMutableMap(raw); derr != nil {
			log.Printf("bilibili: publish hotboard %s error: %v", key, derr)
		} else if err := publish(m); err != nil {
			log.Printf("bilibili: publish hotboard %s error: %v", key, err)
		}
		published++
	}
	return published
}
