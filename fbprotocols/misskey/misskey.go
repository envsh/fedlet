package misskey

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	pubfn_   func([]byte) error
	muClient sync.Mutex
	curHost  string
	curToken string
	curTL    string
)

func SetPublishInfo(pubfn func([]byte) error) {
	pubfn_ = pubfn
}

func publish(data []byte) error {
	if pubfn_ == nil {
		return nil
	}
	return pubfn_(data)
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
			ev := map[string]any{
				"type":   "misskey_note",
				"id":     n.ID,
				"text":   n.Text,
				"user":   n.User.Username,
				"name":   n.User.Name,
				"userId": n.UserID,
				"time":   n.CreatedAt,
			}
			data, _ := json.Marshal(ev)
			log.Printf("misskey: @%s: %s", n.User.Username, truncate(n.Text, 80))
			if err := publish(data); err != nil {
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

func Send(to, msg, msgType string) error {
	if msg == "" {
		return fmt.Errorf("misskey: empty message")
	}
	visibility := to
	if visibility == "" {
		visibility = "home"
	}
	muClient.Lock()
	host, token := curHost, curToken
	muClient.Unlock()
	if host == "" || token == "" {
		return fmt.Errorf("misskey: not configured")
	}
	log.Printf("misskey: send [%s]: %s", visibility, truncate(msg, 80))
	return SendNote(host, token, msg, visibility)
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

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}
