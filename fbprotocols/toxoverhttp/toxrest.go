package toxoverhttp

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"fmt"
)

/////
func publish(data []byte) error {
	if pubfn_ == nil {
		return fmt.Errorf("pubfn not set")
	}

	return pubfn_(data)
}

var pubfn_ func([]byte) error

func SetPublishInfo(pubfn func([]byte) error) {
	pubfn_ = pubfn
}
func Start(info string) {
	if info != "" {
		toxrest_url = info
	}
	go poll_toxrest()
}

////

// base URL, code appends /api/events?after=N at runtime
var toxrest_url = "http://127.0.0.1:8181"

type Event struct {
	ID        uint64    `json:"event_id"`
	Type      string    `json:"event_type"`
	Data      string    `json:"data"`
	Timestamp time.Time `json:"timestamp"`
}

func poll_toxrest() {
	// var channel_name = "reddit"

	after := uint64(0)
	client := &http.Client{Timeout: 60 * time.Second}

	for cnter := 0;; cnter++ {
		u, err := url.Parse(toxrest_url)
		if err != nil {
			log.Println("url parse error:", err)
			time.Sleep(5 * time.Second)
			continue
		}
		u = u.JoinPath("/api/events")
		q := u.Query()
		q.Set("after", strconv.FormatUint(after, 10))
		u.RawQuery = q.Encode()
		url := u.String()
		// log.Println("poll_toxrest: GET after=", after)
		resp, err := client.Get(url)
		if err != nil {
			log.Println("GET error:", err)
			time.Sleep(5 * time.Second)
			continue
		}
		// log.Println("poll_toxrest: status=", resp.Status)

		if nextIDStr := resp.Header.Get("X-Server-Next-Id"); nextIDStr != "" {
			if nid, err := strconv.ParseUint(nextIDStr, 10, 64); err == nil {
				// log.Println("poll_toxrest: next_id=", nid)
				after = nid
			}
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		log.Println("response body:", string(body))

		var events []Event
		if err := json.Unmarshal(body, &events); err != nil {
			var errResp struct {
				Error string `json:"error"`
			}
			if err2 := json.Unmarshal(body, &errResp); err2 == nil && errResp.Error != "" {
				log.Println("server error:", errResp.Error)
			} else {
				log.Println("decode error:", err)
			}
			time.Sleep(5 * time.Second)
			continue
		}

		if len(events) == 0 {
			log.Println("no events (timeout)", after, cnter)
		} else {
			// log.Println("got", len(events), "events")
		}

		published := 0
		for _, ev := range events {
			if ev.ID >= after {
				after = ev.ID + 1
			}
			evJSON, err := json.Marshal(ev)
			if err != nil {
				log.Println("marshal error:", err)
				continue
			}
			if err := publish(evJSON); err != nil {
				log.Println("publish error:", err)
			} else {
				published++
			}
		}
		if len(events) > 0 {
			// log.Println("published", published, "/", len(events))
		}
	}
}
