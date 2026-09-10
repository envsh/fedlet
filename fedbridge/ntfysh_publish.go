package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

var ntfyshTopic string
var ntfyshServer string

// ntfy dual-publish state: backoff window (guarded by ntfyMu), monotonic
// send counters, and the escalating default backoff used when ntfy.sh omits
// the Retry-After header (observed: most 429s from ntfy.sh do not carry it).
var (
	ntfyMu         sync.Mutex
	ntfyRetryAfter time.Time
	ntfyBackoff    = 60 * time.Second
	ntfyBackoffMin = 60 * time.Second
	ntfyBackoffMax = 15 * time.Minute
	ntfySentOK     atomic.Int64
	ntfySentFail   atomic.Int64
	ntfySkipped    atomic.Int64
)

// doNtfyPublishFn is an indirection point so the skip/backoff/counter logic
// can be unit-tested offline.
var doNtfyPublishFn = doNtfyPublish

func publishNtfy(protocol, channel string, v any) {
	if ntfyshTopic == "" {
		return
	}
	ntfyMu.Lock()
	inBackoff := time.Now().Before(ntfyRetryAfter)
	ntfyMu.Unlock()
	if inBackoff {
		ntfySkipped.Add(1)
		return
	}
	var body string
	var title string
	switch vv := v.(type) {
	case fbshared.UnifiedMessage:
		title = protocol + ":" + channel
		bcc, err := json.Marshal(vv)
		if err != nil {
			panic(err)
		}
		body = string(bcc)
	default:
		data, _ := json.Marshal(v)
		title = protocol + ":" + channel
		body = string(data)
		panic("not support")
	}
	if body == "" {
		return
	}
	url := ntfyshServer + "/" + ntfyshTopic + "?up=1"
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		ntfySentFail.Add(1)
		log.Printf("ntfysh: request error: %v", err)
		return
	}
	req.Header.Set("Title", title)
	req.Header.Set("Tags", protocol)
	status, retryAfter, respBody, err := doNtfyPublishFn(req)
	if err != nil {
		ntfySentFail.Add(1)
		log.Printf("ntfysh: publish error: %v", err)
		return
	}
	if status == 200 {
		ntfySentOK.Add(1)
		ntfyMu.Lock()
		ntfyRetryAfter = time.Time{}
		ntfyBackoff = ntfyBackoffMin
		ntfyMu.Unlock()
		if n := ntfySkipped.Swap(0); n > 0 {
			log.Printf("ntfysh: backoff ended, skipped %d (ok=%d fail=%d)",
				n, ntfySentOK.Load(), ntfySentFail.Load())
		}
		return
	}
	ntfySentFail.Add(1)
	ntfyMu.Lock()
	d := parseRetryAfter(retryAfter)
	if d <= 0 {
		d = ntfyBackoff // 60s → 120s → … (escalates on repeated 429s)
		ntfyBackoff = min(ntfyBackoff*2, ntfyBackoffMax)
	} else {
		ntfyBackoff = ntfyBackoffMin
		d = min(d, time.Hour) // clamp server-provided value
	}
	ntfyRetryAfter = time.Now().Add(d)
	ntfyMu.Unlock()
	log.Printf("ntfysh: status %d retry-after=%s backoff=%v body=%s len=%v",
		status, retryAfter, d, respBody, len(body))
}

// doNtfyPublish performs the real POST, returning status, Retry-After header
// value and response body for logging.
func doNtfyPublish(req *http.Request) (int, string, string, error) {
	resp, err := httpClient30s.Do(req)
	if err != nil {
		return 0, "", "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(resp.Header.Get("Retry-After")), strings.TrimSpace(string(b)), nil
}

// parseRetryAfter decodes a Retry-After header. Per RFC 9110 §10.2.3 it may
// be either a delta-seconds count or an HTTP-date.
func parseRetryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if sec, err := strconv.Atoi(h); err == nil && sec > 0 {
		return time.Duration(sec) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// GET /api/ntfystats — ntfy dual-publish counter snapshot.
func handleNtfyStats(w http.ResponseWriter, r *http.Request) {
	ntfyMu.Lock()
	until := ntfyRetryAfter
	ntfyMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":            ntfySentOK.Load(),
		"fail":          ntfySentFail.Load(),
		"skipped":       ntfySkipped.Load(),
		"in_backoff":    time.Now().Before(until),
		"backoff_until": until.Format(time.RFC3339),
	})
}

func init() {
	http.HandleFunc("/api/ntfystats", handleNtfyStats)
}
