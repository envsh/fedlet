package weibo

// Protocol orchestrator: Start / poll loop / dedupe state / status.
//
// Single anonymous feed that carries three hot boards at once (hotSearch):
//   - realtime       微博热搜榜 (main, ~50 entries)
//   - hotgov         政务热搜 (single featured + hotgovs list)
//   - band_list      文娱榜 (sporadic; published only when non-empty)
//
// No login, no cookies, no signature (verified live 2026-09-16). Poll default
// 60s (the board on the web refreshes continuously, dedupe makes it cheap).
// State (dedupe, 72h window) persists to ~/.config/fedlet/weibo-state.json.
// Feed errors are surfaced through LastErrs; a failing round never blocks
// the loop. Publish: each entry forwarded verbatim + flat proto_type/cycle_count.

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

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

const (
	defaultInterval = 60 * time.Second
	dedupeExpiry    = 72 * time.Hour
)

// authStatusPublic is reported by AuthStatus: the feed runs without any login.
const authStatusPublic = "public"

var (
	pubfn_   func(any) error
	mu       sync.Mutex
	on       bool
	interval time.Duration
)

// hc is the anonymous HTTP client shared by the boards.
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
	req.Header.Set("Referer", "https://weibo.com/")
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

// weiboState is the persisted dedupe set (section:word -> seen unix ts).
type weiboState struct {
	Hot map[string]int64 `json:"hot"`
}

func newState() *weiboState {
	return &weiboState{Hot: map[string]int64{}}
}

// Start launches the poll loop; a non-positive interval falls back to 60s.
func Start(intervalIn time.Duration) {
	if intervalIn <= 0 {
		intervalIn = defaultInterval
	}
	mu.Lock()
	on = true
	interval = intervalIn
	mu.Unlock()
	go pollLoop()
}

func pollLoop() {
	statusRunning.Store(true)
	statusConnectedSince.Store(time.Now())
	defer statusRunning.Store(false)

	mu.Lock()
	it := interval
	mu.Unlock()
	logPrefix("interval=%s", it)

	state := loadState(stateFilePath())
	next := time.Now().Add(it)

	tick := it
	if tick > time.Minute {
		tick = time.Minute
	}

	for {
		now := time.Now()
		if now.After(next) {
			hotRound(state)
			next = now.Add(it)
		}
		pruneState(state, time.Now())
		saveState(stateFilePath(), state)
		time.Sleep(tick)
	}
}

// fetchHotlistFn is an indirection point so rounds can be tested offline.
var fetchHotlistFn = FetchHotlist

// boardEntry is a normalized entry fetched from one of the three boards.
type boardEntry struct {
	section string
	word    string
	note    string
	rank    int64
	label   string
	heat    string
	raw     json.RawMessage
}

// hotBoardEntries flattens the three boards into ordered entries.
func hotBoardEntries(r *WeiboResp) []boardEntry {
	var out []boardEntry
	for i := range r.Data.Realtime {
		it := &r.Data.Realtime[i]
		out = append(out, boardEntry{
			section: "realtime", word: it.Word, note: it.Note,
			rank: it.Realpos, label: WeiboLabel(it), heat: WeiboHot(it),
			raw: it.Raw,
		})
	}
	if g := r.Data.Hotgov; g.Word != "" {
		out = append(out, boardEntry{
			section: "hotgov", word: g.Word, note: g.Note, rank: g.Pos, label: g.IconDesc,
			raw: g.Raw,
		})
	}
	for i := range r.Data.Hotgovs {
		it := &r.Data.Hotgovs[i]
		out = append(out, boardEntry{
			section: "hotgov", word: it.Word, note: it.Note, rank: it.Pos, label: WeiboLabel(it),
			raw: it.Raw,
		})
	}
	for i := range r.Data.BandList {
		it := &r.Data.BandList[i]
		out = append(out, boardEntry{
			section: "band", word: it.Word, note: it.Note,
			rank: it.Realpos, label: WeiboLabel(it), heat: WeiboHot(it),
			raw: it.Raw,
		})
	}
	return out
}

// hotRound publishes entries that newly appeared on any of the boards.
func hotRound(state *weiboState) {
	resp, err := fetchHotlistFn()
	if err != nil {
		logPrefix("hotlist error: %v", err)
		pushError(err)
		return
	}
	entries := hotBoardEntries(resp)
	if len(entries) == 0 {
		logPrefix("hot round empty")
		return
	}
	now := time.Now()
	published := 0
	for i := range entries {
		e := &entries[i]
		key := e.section + ":" + e.word
		if key == ":" {
			continue
		}
		if _, seen := state.Hot[key]; seen {
			continue
		}
		state.Hot[key] = now.Unix()
		title := e.word
		logPrefix("hot #%d %s %s %s", i+1, e.section, WeiboLink(e.word), truncate(title, 60))
		raw, aerr := fbshared.InsertFlatFields(e.raw, map[string]any{
			"proto_type":  "weibo_hot",
			"cycle_count": len(entries),
			"url":         WeiboLink(e.word),
		})
		if aerr != nil {
			logPrefix("publish hot %s error: %v", key, aerr)
		} else if err := publish(raw); err != nil {
			logPrefix("publish hot %s error: %v", key, err)
		}
		published++
	}
	if published > 0 {
		logPrefix("hot round published %d new entries", published)
	} else {
		logPrefix("hot round no change")
	}
}

// ---- dedupe state persistence ----

func stateFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "fedlet", "weibo-state.json")
}

func loadState(path string) *weiboState {
	data, err := os.ReadFile(path)
	if err != nil {
		return newState()
	}
	var s weiboState
	if err := json.Unmarshal(data, &s); err != nil {
		logPrefix("load state parse error: %v", err)
		return newState()
	}
	if s.Hot == nil {
		s.Hot = map[string]int64{}
	}
	return &s
}

func saveState(path string, s *weiboState) {
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

func pruneState(s *weiboState, now time.Time) {
	cutoff := now.Add(-dedupeExpiry).Unix()
	for k, seen := range s.Hot {
		if seen < cutoff {
			delete(s.Hot, k)
		}
	}
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

// AuthStatus is always "public": the feed runs anonymously.
func AuthStatus() string { return authStatusPublic }

// AuthUser identifies the anonymous backend.
func AuthUser() string { return "weibo" }

// ---- shared small helpers ----

// ua mirrors the browser User-Agent used across fedlet web clients.
const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

func logPrefix(format string, args ...any) {
	log.Printf("weibo: "+format, args...)
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
