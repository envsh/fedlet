package hongguo

// hongguo.go — lifecycle and poll loop for the 红果短剧 hot board.
//
// Like the xhs backend this module has a start/stop poll loop with a
// dedupe window and an error journal; unlike xhs there is no auth surface
// (the source page is fully public and anonymous), so AuthStatus stays
// "anonymous" and AuthStart/AuthCancel are absent by design.

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// publishFn forwards one published value to the bridge sink.
type publishFn func(any) error

var (
	stateMu sync.RWMutex
	running bool
	stopped chan struct{}

	pulbMu sync.RWMutex
	sink   publishFn

	seenMu sync.Mutex
	seen   map[string]time.Time

	stateFile   string
	stateLoaded bool

	errsMu sync.RWMutex
	lastErrs []error

	connSinceMu sync.RWMutex
	connectedSince time.Time
)

// SetPublishInfo installs (or clears, with nil) the forwarding sink.
func SetPublishInfo(fn func(any) error) {
	pulbMu.Lock()
	defer pulbMu.Unlock()
	sink = fn
}

// Start launches the poll loop. It is idempotent: a second Start while
// running is a no-op more than a restart.
func Start(interval time.Duration) {
	stateMu.Lock()
	if running {
		stateMu.Unlock()
		return
	}
	running = true
	stopped = make(chan struct{})
	connSinceMu.Lock()
	connectedSince = time.Now()
	connSinceMu.Unlock()
	stateMu.Unlock()

	if interval <= 0 {
		interval = 15 * time.Minute
	}
	loadState()
	log.Printf("hongguo: polling every %s (seen=%d persisted)", interval, len(seen))
	go loop(interval)
}

// Stop halts the loop.
func Stop() {
	stateMu.Lock()
	if !running {
		stateMu.Unlock()
		return
	}
	running = false
	close(stopped)
	stateMu.Unlock()
}

func isRunning() bool {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return running
}

// IsRunning reports whether the loop is active.
func IsRunning() bool { return isRunning() }

// AuthStatus reports the (constant) anonymous status; there is no login.
func AuthStatus() string { return "anonymous" }

// ConnectedSince is when the current loop round started.
func ConnectedSince() time.Time {
	connSinceMu.RLock()
	defer connSinceMu.RUnlock()
	return connectedSince
}

// LastErrs returns a copy of the recent error journal.
func LastErrs() []error {
	errsMu.RLock()
	defer errsMu.RUnlock()
	return append([]error(nil), lastErrs...)
}

func loop(interval time.Duration) {
	pollOnce()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-stopped:
			return
		case <-ticker.C:
			pollOnce()
		}
	}
}

func pollOnce() {
	items, err := FetchHotBoard()
	if err != nil {
		recordErr(err)
		return
	}
	now := time.Now()
	pulbMu.RLock()
	fn := sink
	pulbMu.RUnlock()

	newCount := 0
	for i := range items {
		it := items[i]
		if !markSeen(it.SeriesID, now) {
			continue
		}
		newCount++
		if fn != nil {
			if perr := fn(it); perr != nil {
				recordErr(perr)
			}
		}
	}
	saveState()
	log.Printf("hongguo: round total=%d new=%d", len(items), newCount)
}

// markSeen records a series_id within the 72h dedupe window; returns true for
// a brand-new card that should be published.
func markSeen(id string, now time.Time) bool {
	if id == "" {
		return false
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	if seen == nil {
		seen = make(map[string]time.Time)
	}
	cutoff := now.Add(-72 * time.Hour)
	for k, v := range seen {
		if v.Before(cutoff) {
			delete(seen, k)
		}
	}
	if _, ok := seen[id]; ok {
		return false
	}
	seen[id] = now
	return true
}

func recordErr(err error) {
	if err == nil {
		return
	}
	log.Printf("hongguo: %v", err)
	errsMu.Lock()
	defer errsMu.Unlock()
	lastErrs = append(lastErrs, err)
	if len(lastErrs) > 16 {
		lastErrs = lastErrs[len(lastErrs)-16:]
	}
}

// loadState 装载持久化的 72h seen 窗口,重启后不重播种。
func loadState() {
	path, err := statePath()
	if err != nil {
		return
	}
	stateLoaded = true
	data, err := os.ReadFile(path)
	if err != nil {
		return // first run: 无历史文件,后续 saveState 仍会创建
	}
	var rec struct {
		Seen map[string]time.Time `json:"seen"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return
	}
	cutoff := time.Now().Add(-72 * time.Hour)
	seenMu.Lock()
	defer seenMu.Unlock()
	seen = make(map[string]time.Time)
	for k, v := range rec.Seen {
		if !v.Before(cutoff) {
			seen[k] = v
		}
	}
	stateLoaded = true
}

// saveState 将当前 seen 落盘;只有装载过状态文件才写入,避免污染目录。
func saveState() {
	if !stateLoaded {
		return
	}
	seenMu.Lock()
	rec := struct {
		Seen map[string]time.Time `json:"seen"`
	}{Seen: make(map[string]time.Time, len(seen))}
	for k, v := range seen {
		rec.Seen[k] = v
	}
	seenMu.Unlock()

	path, err := statePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// statePath 解析一次并缓存状态文件路径。
func statePath() (string, error) {
	if stateFile != "" {
		return stateFile, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	stateFile = filepath.Join(home, ".config", "fedlet", "hongguo-state.json")
	return stateFile, nil
}

var errNotRunning = errors.New("hongguo: loop not running")
