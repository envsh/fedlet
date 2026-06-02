package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"
)

var toxrest_url = "http://127.0.0.1:8181/api/events?after="
var channel_name = "reddit"

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
		url := toxrest_url + strconv.FormatUint(after, 10)
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

		var events []Event
		if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
			resp.Body.Close()
			log.Println("decode error:", err)
			continue
		}
		resp.Body.Close()

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
			if err := publish(channel_name, evJSON); err != nil {
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
