package bilibili

// Video recommend feed (x/web-interface/wbi/index/top/feed/rcmd).
//
// This is a STANDALONE pull layer: FetchRecommend is exposed for callers that
// want recommendations on demand. It is NOT wired into pollLoop (no round, no
// dedupe, no automatic publishing), and it NEVER triggers the login gateway —
// without a session it fails fast with ErrNotLoggedIn so the caller decides
// what to do.
//
// The endpoint is WBI-signed (see wbi.go) and needs SESSDATA + the bili_ticket
// best-effort cookie.

import (
	"errors"
	"fmt"
	"log"
)

const rcmdBase = apiHost + "/x/web-interface/wbi/index/top/feed/rcmd"

// rcmdItem is the tolerant shape of rcmd.data.item[].
type rcmdItem struct {
	ID    int64  `json:"id"`
	Bvid  string `json:"bvid"`
	Type  int    `json:"type"`
	Title string `json:"title"`
	Pic   string `json:"pic"`
	Owner struct {
		Mid  int64  `json:"mid"`
		Name string `json:"name"`
	} `json:"owner"`
	Stat struct {
		View int64 `json:"view"`
		Like int64 `json:"like"`
	} `json:"stat"`
	Duration int64 `json:"duration"`
}

func (it *rcmdItem) url() string {
	if it.Bvid != "" {
		return "https://www.bilibili.com/video/" + it.Bvid
	}
	return fmt.Sprintf("https://www.bilibili.com/video/av%d", it.ID)
}

// rcmdData is the envelope of rcmd.data.
type rcmdData struct {
	Item []rcmdItem `json:"item"`
}

// FetchRecommend pulls up to `limit` recommended videos (1..30). It fails fast
// with ErrNotLoggedIn when the session is missing, and never starts the login
// UI. Requires fresh WBI keys (fetched by ensureWarmup via /nav).
func FetchRecommend(limit int) ([]rcmdItem, error) {
	if jar.get("SESSDATA") == "" {
		return nil, ErrNotLoggedIn
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 30 {
		limit = 30
	}
	ensureWarmup()
	if !wbiReady() {
		// One retry: prime the WBI keys from a fresh nav probe.
		if err := refreshWBIKeys(); err != nil {
			return nil, err
		}
	}
	params := map[string]string{
		"ps":         fmt.Sprintf("%d", limit),
		"fresh_type": "4",
	}
	u, err := buildWBIURL(rcmdBase, params)
	if err != nil {
		return nil, err
	}
	var data rcmdData
	if err := getJSON(&data, u, true); err != nil {
		if isWBIStale(err) {
			if err := refreshWBIKeys(); err != nil {
				return nil, err
			}
			u, err = buildWBIURL(rcmdBase, params)
			if err != nil {
				return nil, err
			}
			if err := getJSON(&data, u, true); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	log.Printf("bilibili: recommend round got %d items", len(data.Item))
	return data.Item, nil
}

// refreshWBIKeys re-probes /nav and extracts fresh wbi keys; also best-effort
// re-fetches the bili_ticket for the recommend feed.
func refreshWBIKeys() error {
	nav := map[string]any{}
	if err := getJSONDataRaw(&nav, navURL); err != nil {
		return fmt.Errorf("bilibili: refresh wbi keys: %w", err)
	}
	wbiKeysFromNav(nav)
	biliTicket()
	if !wbiReady() {
		return errors.New("bilibili: wbi keys unavailable after nav probe")
	}
	return nil
}

// isWBIStale flags the couple of server answers that mean "signature rejected"
// rather than a hard failure (the numeric codes may drift — best-effort).
func isWBIStale(err error) bool {
	if err == nil {
		return false
	}
	return containsAny(err.Error(), "-403", "code -403", "w_rid", "wbi")
}
