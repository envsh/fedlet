package toutiao

// Protocol orchestrator: Start / poll loop / dedupe state / status.
//
// Two anonymous feeds (no login, no signature, no cookies):
//   - hot board:  /hot-event/hot-board, ~600s (server caches 5-10 min)
//   - news feed:  /api/pc/feed, ~120s, newest-first, incremental by behot_time
//
// State (dedupe, 72h window) persists to ~/.config/fedlet/toutiao-state.json.
// Feed errors are surfaced through LastErrs; rounds never block each other.

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultHotInterval  = 600 * time.Second
	defaultNewsInterval = 120 * time.Second
	dedupeExpiry        = 72 * time.Hour
)

// authStatusPublic is reported by AuthStatus: both feeds run without any login.
const authStatusPublic = "public"

var (
	pubfn_  func(any) error
	mu      sync.Mutex
	hotOn   bool
	newsOn  bool
	hotInt  time.Duration
	newsInt time.Duration
)

// hc is the anonymous HTTP client shared by both feeds.
var hc = &http.Client{Timeout: 20 * time.Second}

func SetPublishInfo(pubfn func(any) error) {
	pubfn_ = pubfn
}

func publish(v any) error {
	if pubfn_ == nil {
		return nil
	}
	return pubfn_(v)
}

// fetchJSON performs an anonymous GET with browser-ish headers and returns the
// raw response body.
func fetchJSON(rawURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", "https://www.toutiao.com/")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// parseHotlist decodes and validates a hot-board response body.
func parseHotlist(body []byte) (*HotlistResp, error) {
	var r HotlistResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("toutiao hotlist status %q", r.Status)
	}
	return &r, nil
}

// parseNews decodes and validates a feed response body.
func parseNews(body []byte) (*NewsResp, error) {
	var r NewsResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if r.Message != "success" {
		return nil, fmt.Errorf("toutiao news message %q", r.Message)
	}
	return &r, nil
}

// toutiaoState is the persisted dedupe set (key -> seen unix ts).
type toutiaoState struct {
	Hotlist map[string]int64 `json:"hotlist"`
	News    map[string]int64 `json:"news"`
	// LastBehot is the greatest behot_time already published; the news round
	// skips anything at or below it so a long-downtime backlog is not republished.
	LastBehot int64 `json:"last_behot"`
}

func newState() *toutiaoState {
	return &toutiaoState{
		Hotlist: map[string]int64{},
		News:    map[string]int64{},
	}
}

// Start launches the poll loop. hot/news enable the two feeds; zero intervals
// fall back to the defaults (600s / 120s).
func Start(hot, news bool, hotInterval, newsInterval time.Duration) {
	if hotInterval <= 0 {
		hotInterval = defaultHotInterval
	}
	if newsInterval <= 0 {
		newsInterval = defaultNewsInterval
	}
	mu.Lock()
	hotOn = hot
	newsOn = news
	hotInt = hotInterval
	newsInt = newsInterval
	mu.Unlock()
	go pollLoop()
}

func pollLoop() {
	statusRunning.Store(true)
	statusConnectedSince.Store(time.Now())
	defer statusRunning.Store(false)

	mu.Lock()
	hot := hotOn
	news := newsOn
	hi := hotInt
	ni := newsInt
	mu.Unlock()
	logPrefix("hot=%v news=%v intervals=%s/%s", hot, news, hi, ni)

	state := loadState(stateFilePath())
	if !hot && !news {
		logPrefix("both feeds disabled, nothing to poll")
		return
	}

	now := time.Now()
	nextHot := now.Add(hi)
	nextNews := now.Add(ni)

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

// fetchHotlistFn / fetchNewsFn are indirection points so rounds can be tested
// offline (mirrors the probeSession pattern in the xhs backend).
var (
	fetchHotlistFn = FetchHotlist
	fetchNewsFn    = FetchNews
)

// hotRound publishes entries that newly appeared on the hot board.
func hotRound(state *toutiaoState) {
	resp, err := fetchHotlistFn()
	if err != nil {
		logPrefix("hotlist error: %v", err)
		pushError(err)
		return
	}
	now := time.Now()
	published := 0
	for i := range resp.Data {
		it := &resp.Data[i]
		key := it.Key()
		if key == "" {
			continue
		}
		if _, seen := state.Hotlist[key]; seen {
			continue
		}
		state.Hotlist[key] = now.Unix()
		title := HotlistTitle(it)
		logPrefix("hotlist #%d %s %s", i+1, key, truncate(title, 80))
		payload := map[string]any{
			"kind":         "hotlist",
			"rank":         i,
			"id":           key,
			"title":        title,
			"detail":       HotlistDetail(it),
			"hot":          HotlistHot(it),
			"url":          HotlistLink(it),
			"count":        len(resp.Data),
			"published_at": now.Unix(),
		}
		if err := publish(payload); err != nil {
			logPrefix("publish hotlist %s error: %v", key, err)
		}
		published++
	}
	if published > 0 {
		logPrefix("hotlist round published %d new entries", published)
	} else {
		logPrefix("hotlist round no change")
	}
}

// newsRound publishes feed articles newer than the last seen behot watermark.
func newsRound(state *toutiaoState) {
	resp, err := fetchNewsFn(0)
	if err != nil {
		logPrefix("news error: %v", err)
		pushError(err)
		return
	}
	if len(resp.Data) == 0 {
		logPrefix("news round empty")
		return
	}
	newest := resp.Data[0].BehotTime
	if newest <= state.LastBehot {
		logPrefix("news round no change (newest %d <= seen %d)", newest, state.LastBehot)
		return
	}
	now := time.Now()
	published := 0
	for i := range resp.Data {
		it := &resp.Data[i]
		if it.IsAd() {
			continue
		}
		key := it.Key()
		if key == "" {
			continue
		}
		// Skip anything already past our watermark (covers restart and is the
		// dedupe backstop with the map below).
		if it.BehotTime <= state.LastBehot {
			continue
		}
		if _, seen := state.News[key]; seen {
			continue
		}
		state.News[key] = now.Unix()
		title := it.Title
		logPrefix("news #%d %s %s", i+1, key, truncate(title, 80))
		payload := map[string]any{
			"kind":         "news",
			"id":           key,
			"title":        title,
			"source":       it.Source,
			"tag":          NewsTag(it),
			"is_video":     NewsVideo(it),
			"comments":     NewsComments(it),
			"url":          NewsLink(it),
			"image":        it.ImageURL,
			"published":    it.BehotTime,
			"count":        len(resp.Data),
			"published_at": now.Unix(),
		}
		if err := publish(payload); err != nil {
			logPrefix("publish news %s error: %v", key, err)
		}
		published++
	}
	if newest > state.LastBehot {
		state.LastBehot = newest
	}
	if published > 0 {
		logPrefix("news round published %d new articles (watermark %d)", published, state.LastBehot)
	} else {
		logPrefix("news round no change (watermark %d)", state.LastBehot)
	}
}

// ---- dedupe state persistence ----

func stateFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "fedlet", "toutiao-state.json")
}

func loadState(path string) *toutiaoState {
	data, err := os.ReadFile(path)
	if err != nil {
		return newState()
	}
	var s toutiaoState
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

func saveState(path string, s *toutiaoState) {
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

func pruneState(s *toutiaoState, now time.Time) {
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

// AuthStatus is always "public": both feeds run anonymously.
func AuthStatus() string { return authStatusPublic }

// AuthUser identifies the anonymous backend.
func AuthUser() string { return "toutiao" }

// ---- shared small helpers ----

// ua mirrors the browser User-Agent used across fedlet web clients.
const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

func logPrefix(format string, args ...any) {
	log.Printf("toutiao: "+format, args...)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// parseInt tolerantly parses a base-10 int64 (used for the often-string heat).
func parseInt(s string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v
}

// formatHeat renders big numbers compactly (13830954 -> 1383.1w).
func formatHeat(v int64) string {
	if v >= 10000 {
		w := float64(v) / 10000
		return fmt.Sprintf("%.1fw", w)
	}
	return fmt.Sprintf("%d", v)
}
