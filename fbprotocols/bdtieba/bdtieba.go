package bdtieba

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	dedupeExpiry    = 72 * time.Hour
	minReqInterval  = 60 * time.Second
	defaultInterval = 600 * time.Second
)

var defaultKws = []string{"linux", "android", "个人电脑", "游戏吧", "gpt", "ai人工智能"}

var (
	pubfn_      func(any) error
	muClient    sync.Mutex
	curKws      []string
	curInterval time.Duration
)

func SetPublishInfo(pubfn func(any) error) {
	pubfn_ = pubfn
}

func publish(v any) error {
	if pubfn_ == nil {
		return nil
	}
	return pubfn_(v)
}

type stateData map[int64]int64

func Start(kws []string, interval time.Duration) {
	if len(kws) == 0 {
		kws = defaultKws
	}
	if interval <= 0 {
		interval = defaultInterval
	}
	muClient.Lock()
	curKws = append([]string(nil), kws...)
	curInterval = interval
	muClient.Unlock()

	go pollLoop()
}

func pollLoop() {
	statusRunning.Store(true)
	statusConnectedSince.Store(time.Now())
	defer statusRunning.Store(false)

	muClient.Lock()
	kws := append([]string(nil), curKws...)
	interval := curInterval
	muClient.Unlock()
	log.Printf("bdtieba: polling %d forums every %s", len(kws), interval)

	statePath := stateFilePath()
	state := loadState(statePath)
	now := time.Now()
	pruneState(state, now)
	if len(state) > 0 {
		log.Printf("bdtieba: loaded %d tids in dedupe window", len(state))
	}

	for {
		for _, kw := range kws {
			data, err := FetchFrs(kw, 1, 30)
			if err != nil {
				log.Printf("bdtieba: poll %q error: %v", kw, err)
				pushError(err)
				time.Sleep(minReqInterval)
				continue
			}

			checked, dup, n := 0, 0, 0
			for i := range data.ThreadList {
				t := &data.ThreadList[i]
				if t.IsTop == 1 {
					continue
				}
				checked++
				if _, seen := state[t.Tid]; seen {
					dup++
					continue
				}

				n++
				log.Printf("bdtieba: [%s] %s (tid=%d reply=%d) %s",
					forumName(data.Forum, kw), truncate(t.Title, 80), t.Tid, t.ReplyNum, authorNick(t.Author))
				if err := publish(publishPayload(data.Forum, t)); err != nil {
					log.Printf("bdtieba: publish %q tid=%d error: %v", kw, t.Tid, err)
				}
				state[t.Tid] = time.Now().Unix()
			}

			now := time.Now()
			pruneState(state, now)
			saveState(statePath, state)
			if dup > 0 {
				log.Printf("bdtieba: [%s] dedupe skipped %d/%d (new=%d)", forumName(data.Forum, kw), dup, checked, n)
			}
			time.Sleep(minReqInterval)
		}
		time.Sleep(interval)
	}
}

// publishPayload builds the raw (non-unified) payload sent to downstream.
func publishPayload(f Forum, t *Thread) map[string]any {
	return map[string]any{
		"forum":  f,
		"thread": t,
	}
}

func forumName(f Forum, kw string) string {
	if f.Name != "" {
		return f.Name
	}
	return kw
}

func authorNick(a *Author) string {
	if a == nil {
		return ""
	}
	if a.ShowNickname != "" {
		return a.ShowNickname
	}
	return a.NameShow
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

func pruneState(state stateData, now time.Time) {
	cutoff := now.Add(-dedupeExpiry).Unix()
	for tid, seen := range state {
		if seen < cutoff {
			delete(state, tid)
		}
	}
}

func stateFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "fedlet", "bdtieba-state.json")
}

func loadState(path string) stateData {
	data, err := os.ReadFile(path)
	if err != nil {
		return stateData{}
	}
	var s stateData
	if err := json.Unmarshal(data, &s); err != nil {
		log.Printf("bdtieba: load state parse error: %v", err)
		return stateData{}
	}
	if s == nil {
		return stateData{}
	}
	return s
}

func saveState(path string, s stateData) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		log.Printf("bdtieba: save state marshal error: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		log.Printf("bdtieba: save state mkdir error: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("bdtieba: save state write error: %v", err)
	}
}

// protocol status
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

