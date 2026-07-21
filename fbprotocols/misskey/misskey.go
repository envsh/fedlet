package misskey

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

var (
	pubfn_   func(any) error
	muClient sync.Mutex
	curHost  string
	curToken string
	curTL    string
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

func Start(host, token, timeline string) {
	if host == "" || token == "" {
		log.Println("misskey: host and token required")
		return
	}
	if timeline == "" {
		timeline = "home"
	}
	muClient.Lock()
	curHost = host
	curToken = token
	curTL = timeline
	muClient.Unlock()

	info, err := VerifyToken(host, token)
	if err != nil {
		log.Printf("misskey: token verification failed: %v", err)
		return
	}
	log.Printf("misskey: verified as @%s (%s)", info.Username, info.Name)

	meta, err := FetchMeta(host)
	if err != nil {
		log.Printf("misskey: meta error: %v", err)
	} else {
		log.Printf("misskey: server %s (v%s)", meta.Name, meta.Version)
	}

	go pollLoop()
}

func pollLoop() {
	statusRunning.Store(true)
	statusConnectedSince.Store(time.Now())
	defer statusRunning.Store(false)
	log.Printf("misskey: polling %s timeline every 30s", curTL)

	statePath := stateFilePath()
	state := loadState(statePath)

	firstRun := state.SinceID == ""

	for {
		muClient.Lock()
		host, token, tl := curHost, curToken, curTL
		muClient.Unlock()

		notes, err := FetchTimeline(host, token, tl, state.SinceID)
		if err != nil {
			log.Printf("misskey: poll error: %v", err)
			pushError(err)
			time.Sleep(30 * time.Second)
			continue
		}

		if firstRun {
			if len(notes) > 0 {
				state.SinceID = notes[0].ID
				saveState(statePath, state)
				log.Printf("misskey: initial sync, caught up to %s (%d notes seen)", state.SinceID, len(notes))
			}
			firstRun = false
			time.Sleep(30 * time.Second)
			continue
		}

		for i := len(notes) - 1; i >= 0; i-- {
			n := notes[i]
			if n.Text == "" {
				continue
			}
			log.Printf("misskey: @%s: %s", n.User.Username, truncate(n.Text, 80))
			if err := publish(n); err != nil {
				log.Printf("misskey: publish error: %v", err)
			}
		}

		if len(notes) > 0 {
			state.SinceID = notes[0].ID
			saveState(statePath, state)
		}

		time.Sleep(30 * time.Second)
	}
}

func Send(to, msg, msgType string, filedata []byte, _ *fbshared.MediaDataInfo) (fbshared.SendResult, error) {
	if msg == "" {
		return fbshared.SendResult{}, fmt.Errorf("misskey: empty message")
	}
	visibility := "home"
	switch to {
	case "public", "home", "followers", "specified":
		visibility = to
	}
	muClient.Lock()
	host, token := curHost, curToken
	muClient.Unlock()
	if host == "" || token == "" {
		return fbshared.SendResult{}, fmt.Errorf("misskey: not configured")
	}
	log.Printf("misskey: sending [%s]: %s", visibility, truncate(msg, 80))
	noteID, err := SendNote(host, token, msg, visibility)
	if err == nil {
		log.Printf("misskey: sent note_id=%s", noteID)
	}
	return fbshared.SendResult{MsgID: noteID}, err
}

func stateFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "fedlet", "misskey-state.json")
}

func loadState(path string) *stateData {
	data, err := os.ReadFile(path)
	if err != nil {
		return &stateData{}
	}
	var s stateData
	json.Unmarshal(data, &s)
	return &s
}

func saveState(path string, s *stateData) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		log.Printf("misskey: save state marshal error: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		log.Printf("misskey: save state mkdir error: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("misskey: save state write error: %v", err)
	}
}

// protocol status
var (
	statusRunning        atomic.Bool
	statusConnectedSince atomic.Value // time.Time
	statusReconnTimes    atomic.Int64
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

func IsRunning() bool         { return statusRunning.Load() }
func ConnectedSince() time.Time {
	v := statusConnectedSince.Load()
	if v == nil { return time.Time{} }
	return v.(time.Time)
}
func ReconnTimes() int64      { return statusReconnTimes.Load() }
func LastErrs() []error {
	statusLastErrsMu.Lock()
	defer statusLastErrsMu.Unlock()
	var out []error
	for _, e := range statusLastErrs {
		if e != nil { out = append(out, e) }
	}
	return out
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}


