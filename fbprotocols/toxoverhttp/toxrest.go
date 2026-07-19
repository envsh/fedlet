package toxoverhttp

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
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
	statusRunning.Store(true)
	statusConnectedSince.Store(time.Now())
	defer statusRunning.Store(false)
	// var channel_name = "reddit"

	after := uint64(0)
	client := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives:   true,
			MaxIdleConnsPerHost: -1,
		},
	}

	for cnter := 0;; cnter++ {
		u, err := url.Parse(toxrest_url)
		if err != nil {
			log.Println("url parse error:", err)
			pushError(err)
			time.Sleep(5 * time.Second)
			continue
		}
		u = u.JoinPath("/api/events")
		q := u.Query()
		q.Set("after", strconv.FormatUint(after, 10))
		u.RawQuery = q.Encode()
		url := u.String()
		log.Println(">> poll_toxrest: GET after=", after, url)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			log.Println("create request error:", err)
			pushError(err)
			time.Sleep(5 * time.Second)
			continue
		}
		req.Close = true
		// req.Header.Add("Accept", "application/x-ndjson") // TODO
		resp, err := client.Do(req)
		if err != nil {
			log.Println("toxproto GET error:", err)
			pushError(err)
			time.Sleep(5 * time.Second)
			continue
		}
		_ = resp.Header.Get("X-Server-Next-Id")
		// log.Println("poll_toxrest: status=", resp.Status)

		if nextIDStr := resp.Header.Get("X-Server-Next-Id"); nextIDStr != "" {
			if nid, err := strconv.ParseUint(nextIDStr, 10, 64); err == nil {
				if after > nid || after == 0 {
					log.Println("** poll_toxrest: next_id=", nid, after)
					after = nid
				}
			}
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		log.Println("<< toxproto response body:", after, len(body), string(body))

		var events []Event
		if err := json.Unmarshal(body, &events); err != nil {
			log.Println("toxproto error", after, err)
			var errResp struct {
				Error string `json:"error"`
			}
			if err2 := json.Unmarshal(body, &errResp); err2 == nil && errResp.Error != "" {
				log.Println("server error:", errResp.Error)
			} else {
				log.Println("decode error:", err)
			}
			log.Println("toxproto error", err)
			pushError(err)
			time.Sleep(5 * time.Second)
			continue
		}

		if len(events) == 0 {
			log.Println("no events (timeout)", after, cnter)
		} else {
			log.Println("got", len(events), "events")
		}

		published := 0
		for _, ev := range events {
			// log.Println("**", ev.ID)
			if ev.ID > after {
				after = ev.ID // after means after
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
			um, ok := ev.toUnified(evJSON)
			if ok {
				data, _ := json.Marshal(um)
				publish(data)
			}
		}
		if len(events) > 0 {
			// log.Println("published", published, "/", len(events))
		}
	}
}

func sendToOldAPI(to, msg, msgType string) (int, []byte, error) {
	var target string
	var v url.Values
	switch msgType {
	case "unktox_friend":
		target = toxrest_url + "/api/messages"
		v = url.Values{"friend_id": {to}, "message": {msg}}
	case "unktox_conference":
		target = toxrest_url + "/api/conference_messages"
		v = url.Values{"conference_id": {to}, "message": {msg}}
	case "unktox_group":
		target = toxrest_url + "/api/group_messages"
		v = url.Values{"group_number": {to}, "message_type": {""}, "message": {msg}}
	default:
		return 0, nil, fmt.Errorf("no old API fallback for type %q", msgType)
	}
	log.Printf("toxoverhttp: FALLBACK POST %s %v", target, v)
	resp, err := http.PostForm(target, v)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf("toxoverhttp: FALLBACK response status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	return resp.StatusCode, body, nil
}

// Send 发送消息到 toxhttpd。
//   to:      好友/会议/群组数字 ID
//   msg:     消息正文
//   msgType: 联系人类型常量（"unktox_friend"/"unktox_conference"/"unktox_group"），
//            新 API 直接 POST /api/messages/send?type={msgType}&id={to}&message={msg}，
//            404 时 fallback 到旧 API，按 msgType 映射到不同端点和参数名
func Send(to, msg, msgType string, filedata []byte, _ *fbshared.MediaDataInfo) error {
	if to == "" || msg == "" {
		return fmt.Errorf("toxoverhttp: empty to or message")
	}
	if toxrest_url == "" {
		return fmt.Errorf("toxoverhttp: server URL not configured")
	}
	v := url.Values{"type": {msgType}, "id": {to}, "message": {msg}}
	target := toxrest_url + "/api/messages/send"
	log.Printf("toxoverhttp: POST %s type=%q id=%q msg=%q", target, msgType, to, msg)
	resp, err := http.PostForm(target, v)
	if err != nil {
		return fmt.Errorf("toxoverhttp: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf("toxoverhttp: response status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))

	if resp.StatusCode == http.StatusNotFound {
		code, fbBody, fbErr := sendToOldAPI(to, msg, msgType)
		if fbErr != nil {
			return fmt.Errorf("toxoverhttp: fallback: %w", fbErr)
		}
		if code != http.StatusOK {
			return fmt.Errorf("toxoverhttp: fallback status %d: %s",
				code, strings.TrimSpace(string(fbBody)))
		}
		var fbResult struct{ Error string `json:"error"` }
		if json.Unmarshal(fbBody, &fbResult) == nil && fbResult.Error != "" {
			return fmt.Errorf("toxoverhttp: fallback: %s", fbResult.Error)
		}
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("toxoverhttp: status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result struct{ Error string `json:"error"` }
	if json.Unmarshal(body, &result) == nil && result.Error != "" {
		return fmt.Errorf("toxoverhttp: %s", result.Error)
	}
	return nil
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

func (ev *Event) toUnified(raw []byte) (fbshared.UnifiedMessage, bool) {
	return fbshared.UnifiedMessage{
		Text:      ev.Data,
		MsgFormat: fbshared.FmtText,
		Protocol:  fbshared.ProtoToxOverHttp,
		MsgType:   ev.Type,
		MsgID:     strconv.FormatUint(ev.ID, 10),
		Timestamp: ev.Timestamp.UnixNano(),
		Raw:       raw,
	}, true
}
