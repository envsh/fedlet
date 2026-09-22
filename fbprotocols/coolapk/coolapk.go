package coolapk

// Anonymous Coolapk (酷安) backend: no login, no cookie, weak-signed X-App-Token.
// Two feeds, entries published verbatim (raw JSON + flat proto_type/cycle_count):
//   - hotlist: statList 今日热门, ~600s
//   - news:    digestList 最新动态, ~120s
// Dedupe is id-only, persisted to ~/.config/fedlet/coolapk-state.json (0600, 72h).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

const (
	defaultHotInterval  = 600 * time.Second
	defaultNewsInterval = 120 * time.Second
	dedupeExpiry        = 72 * time.Hour
	baseDataListURL     = "https://api.coolapk.com/v6/page/dataList?url="
	hotFragment         = "#/feed/statList?cacheExpires=300&statType=day&sortField=detailnum&title=今日热门&subTitle=&page=1"
	newsFragment        = "#/feed/digestList?type=0,5,9,8,12,10,11,13&title=最新动态&page=1"
)

const authStatusPublic = "public"

var (
	pubfn_ func(any) error
	mu     sync.Mutex
	hotOn  bool
	newsOn bool
	hotInt time.Duration
	newsInt time.Duration
)

var hc = &http.Client{Timeout: 20 * time.Second}

func SetPublishInfo(pubfn func(any) error) { pubfn_ = pubfn }

func publish(v any) error {
	if pubfn_ == nil {
		return nil
	}
	return pubfn_(v)
}

func fetchJSON(rawURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range appHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// fetchDataList pulls one anonymous feed, returning the raw "data" array elements.
func fetchDataList(fragment string) ([]json.RawMessage, error) {
	body, err := fetchJSON(baseDataListURL + url.QueryEscape(fragment))
	if err != nil {
		return nil, err
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	raw, ok := env["data"]
	if !ok || len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	var data []json.RawMessage
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return data, nil
}

var fetchDataListFn = fetchDataList

// walkData flattens an envelope's data array into publishable raw entries plus
// their ids. Only feed (verbatim) and card (inner entities) are kept.
func walkData(data []json.RawMessage) (raws []json.RawMessage, ids []string) {
	for _, it := range data {
		et, id, ents := peekItem(it)
		switch et {
		case "feed":
			raws = append(raws, it)
			ids = append(ids, id)
		case "card":
			for _, e := range ents {
				eid, _ := e["id"].(string)
				if eid == "" {
					continue
				}
				raw, err := json.Marshal(e)
				if err != nil {
					continue
				}
				raws = append(raws, raw)
				ids = append(ids, eid)
			}
		}
	}
	return
}

// peekItem extracts entityType/id and (for cards) the inner entity maps.
func peekItem(raw json.RawMessage) (et, id string, ents []map[string]any) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return
	}
	et, _ = m["entityType"].(string)
	id, _ = m["id"].(string)
	if arr, ok := m["entities"].([]any); ok {
		for _, v := range arr {
			if em, ok := v.(map[string]any); ok {
				ents = append(ents, em)
			}
		}
	}
	return
}

// coolapkState holds the id-only dedupe sets.
type coolapkState struct {
	Hotlist map[string]int64 `json:"hotlist"`
	News    map[string]int64 `json:"news"`
}

func newState() *coolapkState {
	return &coolapkState{Hotlist: map[string]int64{}, News: map[string]int64{}}
}

func Start(hot, news bool, hotInterval, newsInterval time.Duration) {
	if hotInterval <= 0 {
		hotInterval = defaultHotInterval
	}
	if newsInterval <= 0 {
		newsInterval = defaultNewsInterval
	}
	mu.Lock()
	hotOn, newsOn, hotInt, newsInt = hot, news, hotInterval, newsInterval
	mu.Unlock()
	go pollLoop()
}

func pollLoop() {
	statusRunning.Store(true)
	statusConnectedSince.Store(time.Now())
	defer statusRunning.Store(false)

	mu.Lock()
	hot, news, hi, ni := hotOn, newsOn, hotInt, newsInt
	mu.Unlock()
	logPrefix("hot=%v news=%v intervals=%s/%s", hot, news, hi, ni)

	state := loadState(stateFilePath())
	if !hot && !news {
		logPrefix("both feeds disabled, nothing to poll")
		return
	}

	now := time.Now()
	nextHot, nextNews := now.Add(hi), now.Add(ni)
	tick := ni
	if !news {
		tick = hi
	}
	if tick > time.Minute {
		tick = time.Minute
	}
	for {
		now = time.Now()
		if hot && now.After(nextHot) {
			hotRound(state)
			nextHot = now.Add(hi)
		}
		if news && now.After(nextNews) {
			newsRound(state)
			nextNews = now.Add(ni)
		}
		pruneState(state, time.Now())
		saveState(stateFilePath(), state)
		time.Sleep(tick)
	}
}

func hotRound(state *coolapkState) {
	data, err := fetchDataListFn(hotFragment)
	if err != nil {
		logPrefix("hotlist error: %v", err)
		pushError(err)
		return
	}
	raws, ids := walkData(data)
	logPrefix("hotlist round: %d entries, %d new", len(raws), publishEntries(state.Hotlist, "hotlist", raws, ids))
}

func newsRound(state *coolapkState) {
	data, err := fetchDataListFn(newsFragment)
	if err != nil {
		logPrefix("news error: %v", err)
		pushError(err)
		return
	}
	raws, ids := walkData(data)
	logPrefix("news round: %d entries, %d new", len(raws), publishEntries(state.News, "news", raws, ids))
}

// publishEntries dedupes by id and publishes unseen raws verbatim + flat fields.
func publishEntries(seen map[string]int64, kind string, raws []json.RawMessage, ids []string) int {
	now := time.Now().Unix()
	published := 0
	for i := range raws {
		id := ids[i]
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = now
		raw, aerr := fbshared.InsertFlatFields(raws[i], map[string]any{
			"proto_type":  kind,
			"cycle_count": len(raws),
		})
		if aerr != nil {
			logPrefix("publish %s %s error: %v", kind, id, aerr)
			continue
		}
		if err := publish(raw); err != nil {
			logPrefix("publish %s %s error: %v", kind, id, err)
			continue
		}
		published++
	}
	return published
}

// ---- dedupe state persistence ----

func stateFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "fedlet", "coolapk-state.json")
}

func loadState(path string) *coolapkState {
	data, err := os.ReadFile(path)
	if err != nil {
		return newState()
	}
	var s coolapkState
	if err := json.Unmarshal(data, &s); err != nil {
		logPrefix("load state parse error: %v", err)
		return newState()
	}
	if s.Hotlist == nil {
		s.Hotlist = map[string]int64{}
	}
	if s.News == nil {
		s.News = map[string]int64{}
	}
	return &s
}

func saveState(path string, s *coolapkState) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		logPrefix("save state marshal error: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		logPrefix("save state mkdir error: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		logPrefix("save state write error: %v", err)
	}
}

func pruneState(s *coolapkState, now time.Time) {
	cutoff := now.Add(-dedupeExpiry).Unix()
	prune := func(m map[string]int64) {
		for k, seen := range m {
			if seen < cutoff {
				delete(m, k)
			}
		}
	}
	prune(s.Hotlist)
	prune(s.News)
}

// ---- protocol status ----

var (
	statusRunning        atomic.Bool
	statusConnectedSince atomic.Value
	statusLastErrsMu     sync.Mutex
	statusLastErrs       [3]error
)

func pushError(err error) {
	statusLastErrsMu.Lock()
	statusLastErrs[2] = statusLastErrs[1]
	statusLastErrs[1] = statusLastErrs[0]
	statusLastErrs[0] = err
	statusLastErrsMu.Unlock()
}

func IsRunning() bool { return statusRunning.Load() }

func ConnectedSince() time.Time {
	v := statusConnectedSince.Load()
	if v == nil {
		return time.Time{}
	}
	return v.(time.Time)
}

func LastErrs() []error {
	statusLastErrsMu.Lock()
	defer statusLastErrsMu.Unlock()
	var out []error
	for _, e := range statusLastErrs {
		if e != nil {
			out = append(out, e)
		}
	}
	return out
}

func AuthStatus() string { return authStatusPublic }
func AuthUser() string   { return "coolapk" }

func logPrefix(format string, args ...any) {
	log.Printf("coolapk: "+format, args...)
}