package gomuks

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
	"github.com/gorilla/websocket"
)

/////
func publish(v any) error {
	if pubfn_ == nil {
		return fmt.Errorf("pubfn not set")
	}

	return pubfn_(v)
}

var (
	pubfn_         func(any) error
	muGomuks       sync.Mutex
	gomuksConn     *websocket.Conn
	gomuksSeq      int
	authToken      string
	imageAuthToken string

	pendingMu    sync.Mutex
	pendingSends = map[int]chan error{}
)

func SetPublishInfo(pubfn func(any) error) {
	pubfn_ = pubfn
}

func Start(info string) {
	go poll_gomuks()
}

/////

func init() {
	// go poll_gomuks()
}

const gomuksHost = "127.0.0.1:29325"

func poll_gomuks() {
	statusRunning.Store(true)
	statusConnectedSince.Store(time.Now())
	defer statusRunning.Store(false)

	for {
		username := os.Getenv("GOMUKS_USER")
		password := os.Getenv("GOMUKS_PASS")
		if username == "" || password == "" {
			log.Println("GOMUKS_USER/GOMUKS_PASS not set, retry in 30s")
			time.Sleep(30 * time.Second)
			pushError(fmt.Errorf("GOMUKS_USER/GOMUKS_PASS not set"))
			continue
		}

		authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
		token := doGomuksAuth(authHeader)
		if token == "" {
			time.Sleep(10 * time.Second)
			continue
		}
		muGomuks.Lock()
		authToken = token
		muGomuks.Unlock()

		header := http.Header{}
		header.Set("Cookie", "gomuks_auth=" + token)

		u := fmt.Sprintf("ws://%s/_gomuks/websocket", gomuksHost)
		c, resp, err := websocket.DefaultDialer.Dial(u, header)
		if err != nil {
			if resp != nil {
				log.Println("ws dial error: status=", resp.Status, ", err=", err)
				pushError(err)
			} else {
				log.Println("ws dial error:", err)
				pushError(err)
			}
			time.Sleep(5 * time.Second)
			continue
			}

		muGomuks.Lock()
		gomuksConn = c
		muGomuks.Unlock()
		log.Println("ws connected")
		gomuksEventLoop(c)
		log.Println("ws disconnected, reconnecting...")
		time.Sleep(5 * time.Second)
	}
}

func doGomuksAuth(authHeader string) string {
	url := fmt.Sprintf("http://%s/_gomuks/auth?output=json", gomuksHost)
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		log.Println("auth request error:", err)
		pushError(err)
		return ""
	}
	req.Header.Set("Authorization", authHeader)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Println("auth error:", err)
		pushError(err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		log.Println("auth disabled (204)")
		return ""
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		log.Println("auth failed:", resp.Status, string(body))
		pushError(fmt.Errorf("auth failed: %s %s", resp.Status, string(body)))
		return ""
	}

	var ar struct{ Token string `json:"token"` }
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		log.Println("auth decode error:", err)
		pushError(err)
		return ""
	}
	return ar.Token
}

func gomuksEventLoop(c *websocket.Conn) {
	defer func() {
		muGomuks.Lock()
		gomuksConn = nil
		muGomuks.Unlock()
		c.Close()
	}()

	var lastReceivedID int
	var seq int
	pingTicker := time.NewTicker(15 * time.Second)
	defer pingTicker.Stop()

	msgCh := make(chan []byte, 64)

	go func() {
		for {
			_, msg, err := c.ReadMessage()
			if err != nil {
				log.Println("ws read error:", err)
				pushError(err)
				close(msgCh)
				return
			}
			msgCh <- msg
		}
	}()

	for {
		select {
		case msg, ok := <-msgCh:
			if !ok {
				return
			}
			var resp struct {
				Command   string          `json:"command"`
				RequestID int             `json:"request_id"`
				Data      json.RawMessage `json:"data"`
			}
			if json.Unmarshal(msg, &resp) == nil {
				if resp.RequestID == 0 && resp.Command == "image_auth_token" {
					var tok string
					if json.Unmarshal(resp.Data, &tok) == nil {
						muGomuks.Lock()
						imageAuthToken = tok
						muGomuks.Unlock()
						log.Printf("gomuks: received image_auth_token")
					}
					continue
				}
				pendingMu.Lock()
				ch, ok := pendingSends[resp.RequestID]
				pendingMu.Unlock()
				if ok {
					if resp.Command == "error" {
						var errStr string
						json.Unmarshal(resp.Data, &errStr)
						ch <- fmt.Errorf("gomuks: %s", errStr)
					} else {
						ch <- nil
					}
					continue
				}
			}
			if err := publish(json.RawMessage(msg)); err != nil {
				log.Println("publish error:", err)
			}
			um, ok, err := gomuksMsgToUnified(msg)
			if err != nil {
				log.Println("gomuks: unified translate error:", err)
			} else if ok {
				publish(um)
			}

		case <-pingTicker.C:
			seq++
			ping := map[string]any{
				"command":    "ping",
				"request_id": seq,
				"data": map[string]any{
					"last_received_id": lastReceivedID,
				},
			}
			data, _ := json.Marshal(ping)
			if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
				log.Println("ping error:", err)
				pushError(err)
				return
			}
		}
	}
}

func sendGomuksUpload(filedata []byte, fileinfo *fbshared.MediaDataInfo) (json.RawMessage, error) {
	muGomuks.Lock()
	token := authToken
	muGomuks.Unlock()

	u := fmt.Sprintf("http://%s/_gomuks/upload?encrypt=false&filename=%s",
		gomuksHost, url.QueryEscape(fileinfo.Filename))

	req, err := http.NewRequest("POST", u, bytes.NewReader(filedata))
	if err != nil {
		return nil, fmt.Errorf("gomuks: upload request: %w", err)
	}
	req.Header.Set("Cookie", "gomuks_auth="+token)
	if fileinfo.MimeType != "" {
		req.Header.Set("Content-Type", fileinfo.MimeType)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gomuks: upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gomuks: upload failed: %s: %s", resp.Status, string(body))
	}

	var content json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&content); err != nil {
		return nil, fmt.Errorf("gomuks: upload response decode: %w", err)
	}
	return content, nil
}

func Send(roomID, msg, msgType string, filedata []byte, fileinfo *fbshared.MediaDataInfo) error {
	log.Printf("gomuks: Send roomID=%q msg=%q msgType=%q", roomID, msg, msgType)
	if roomID == "" || msg == "" {
		return fmt.Errorf("gomuks: empty roomID or message")
	}

	if len(filedata) > 0 {
		if fileinfo == nil {
			return fmt.Errorf("gomuks: filedata present but fileinfo is nil")
		}

		content, err := sendGomuksUpload(filedata, fileinfo)
		if err != nil {
			return fmt.Errorf("gomuks: upload: %w", err)
		}

		var mapped map[string]any
		if err := json.Unmarshal(content, &mapped); err != nil {
			return fmt.Errorf("gomuks: upload_media response decode: %w", err)
		}

		muGomuks.Lock()
		conn := gomuksConn
		gomuksSeq++
		seq := gomuksSeq
		muGomuks.Unlock()
		if conn == nil {
			return fmt.Errorf("gomuks: not connected")
		}

		cmd := map[string]any{
			"command":    "send_message",
			"request_id": seq,
			"data": map[string]any{
				"room_id":      roomID,
				"text":         msg,
				"base_content": mapped,
			},
		}
		data, _ := json.Marshal(cmd)

		ch := make(chan error, 1)
		pendingMu.Lock()
		pendingSends[seq] = ch
		pendingMu.Unlock()
		defer func() {
			pendingMu.Lock()
			delete(pendingSends, seq)
			pendingMu.Unlock()
		}()

		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return fmt.Errorf("gomuks: send file msg write: %w", err)
		}

		select {
		case err := <-ch:
			if err != nil {
				return fmt.Errorf("gomuks: send file: %w", err)
			}
			log.Printf("gomuks: sent file %s to %s", fileinfo.Filename, roomID)
			return nil
		case <-time.After(30 * time.Second):
			return fmt.Errorf("gomuks: send file msg timeout")
		}
	}

	muGomuks.Lock()
	conn := gomuksConn
	gomuksSeq++
	seq := gomuksSeq
	muGomuks.Unlock()
	if conn == nil {
		return fmt.Errorf("gomuks: not connected")
	}
	cmd := map[string]any{
		"command":    "send_message",
		"request_id": seq,
		"data": map[string]any{
			"room_id": roomID,
			"text":    msg,
		},
	}
	data, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("gomuks: marshal error: %w", err)
	}

	ch := make(chan error, 1)
	pendingMu.Lock()
	pendingSends[seq] = ch
	pendingMu.Unlock()
	defer func() {
		pendingMu.Lock()
		delete(pendingSends, seq)
		pendingMu.Unlock()
	}()

	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return err
	}

	select {
	case err := <-ch:
		return err
	case <-time.After(30 * time.Second):
		return fmt.Errorf("gomuks: send timeout")
	}
}

func DownloadMedia(mxcURL string) (io.ReadCloser, string, error) {
	const pfx = "mxc://"
	if !strings.HasPrefix(mxcURL, pfx) {
		return nil, "", fmt.Errorf("gomuks: not mxc: %s", mxcURL)
	}
	rest := mxcURL[len(pfx):]
	u := fmt.Sprintf("http://%s/_gomuks/media/%s?encrypted=false", gomuksHost, rest)
	muGomuks.Lock()
	tok := imageAuthToken
	muGomuks.Unlock()
	if tok != "" {
		u += "&image_auth=" + url.QueryEscape(tok)
	}
	resp, err := http.Get(u)
	if err != nil {
		return nil, "", fmt.Errorf("gomuks: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", fmt.Errorf("gomuks: download status %d", resp.StatusCode)
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
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

func gomuksMsgToUnified(msg []byte) (fbshared.UnifiedMessage, bool, error) {
	var resp struct {
		Command string          `json:"command"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(msg, &resp); err != nil {
		return fbshared.UnifiedMessage{}, false, err
	}

	if resp.Command != "event" {
		return fbshared.UnifiedMessage{}, false, nil
	}

	var ev struct {
		EventID        string `json:"event_id"`
		Sender         string `json:"sender"`
		RoomID         string `json:"room_id"`
		OriginServerTs int64  `json:"origin_server_ts"`
		Content        *struct {
			Body    string `json:"body"`
			MsgType string `json:"msgtype"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resp.Data, &ev); err != nil {
		return fbshared.UnifiedMessage{}, false, err
	}

	if ev.Content == nil || ev.Content.MsgType != "m.text" {
		return fbshared.UnifiedMessage{}, false, nil
	}

	um := fbshared.UnifiedMessage{
		Protocol:  fbshared.ProtoGomuks,
		MsgID:     ev.EventID,
		UserID:    ev.Sender,
		Username:  ev.Sender,
		ChatID:    ev.RoomID,
		MsgType:   fbshared.MsgTypeCreate,
		MsgFormat: fbshared.FmtText,
		Timestamp: time.Now().UnixNano(),
	}
	if ev.Content != nil {
		um.Text = ev.Content.Body
	}
	if ev.OriginServerTs > 0 {
		um.Timestamp = ev.OriginServerTs * 1000000
	}
	um.Raw = msg
	return um, true, nil
}
