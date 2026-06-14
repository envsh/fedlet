package matrixlite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"
)

const slidingSyncEndpoint = "/_matrix/client/unstable/org.matrix.simplified_msc3575/sync"

type Client struct {
	baseURL     string
	accessToken string
	userID      string
	deviceID    string
	nextBatch   string
	slidingPos  string
	useSliding  bool
	txnID       int64
	hc          *http.Client
}

func Login(server, user, password string) (*Client, error) {
	c := &Client{
		baseURL: server,
		hc: &http.Client{
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
		},
	}

	if err := c.doLogin(user, password); err != nil {
		return nil, err
	}
	c.detectSlidingSync()

	return c, nil
}

type loginReq struct {
	Type       string `json:"type"`
	User       string `json:"user"`
	Password   string `json:"password"`
	DeviceName string `json:"initial_device_display_name,omitempty"`
}

type loginResp struct {
	UserID      string `json:"user_id"`
	AccessToken string `json:"access_token"`
	DeviceID    string `json:"device_id"`
}

func (c *Client) doLogin(user, password string) error {
	body, _ := json.Marshal(loginReq{
		Type:       "m.login.password",
		User:       user,
		Password:   password,
		DeviceName: "fedlet-bridge",
	})
	resp, err := c.doRequest(http.MethodPost, "/_matrix/client/v3/login", body)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var lr loginResp
	if err := json.Unmarshal(raw, &lr); err != nil {
		return fmt.Errorf("login decode: %w", err)
	}
	if lr.AccessToken == "" {
		return fmt.Errorf("login: no access_token: %s", string(raw))
	}
	c.accessToken = lr.AccessToken
	c.userID = lr.UserID
	c.deviceID = lr.DeviceID
	return nil
}

type versionsResp struct {
	UnstableFeatures map[string]bool `json:"unstable_features"`
}

func (c *Client) detectSlidingSync() {
	resp, err := c.doRequest(http.MethodGet, "/_matrix/client/v3/versions", nil)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var vr versionsResp
	if json.Unmarshal(raw, &vr) != nil {
		return
	}
	c.useSliding = vr.UnstableFeatures["org.matrix.simplified_msc3575"]
}

func (c *Client) Sync(timeout time.Duration) ([]Event, error) {
	if c.useSliding {
		events, err := c.slidingSync(timeout)
		if err != nil {
			return c.normalSync(timeout)
		}
		return events, nil
	}
	return c.normalSync(timeout)
}

type slidingSyncReq struct {
	Lists          map[string]slidingList `json:"lists"`
	RoomSubscriptions map[string]interface{} `json:"room_subscriptions,omitempty"`
}

type slidingList struct {
	Ranges         [][]int       `json:"ranges"`
	TimelineLimit  int           `json:"timeline_limit"`
	RequiredState  [][]string    `json:"required_state,omitempty"`
	BumpEventTypes []string      `json:"bump_event_types,omitempty"`
}

type slidingRoom struct {
	Timeline []json.RawMessage `json:"timeline"`
}

type slidingResp struct {
	Pos   string                 `json:"pos"`
	Rooms map[string]slidingRoom `json:"rooms,omitempty"`
}

func (c *Client) slidingSync(timeout time.Duration) ([]Event, error) {
	u := c.baseURL + slidingSyncEndpoint
	q := url.Values{}
	if c.slidingPos != "" {
		q.Set("pos", c.slidingPos)
	}
	if timeout > 0 {
		q.Set("timeout", strconv.Itoa(int(timeout.Milliseconds())))
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}

	body, _ := json.Marshal(slidingSyncReq{
		Lists: map[string]slidingList{
			"all": {
				Ranges:        [][]int{{0, 99}},
				TimelineLimit: 10,
			},
		},
	})

	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var sr slidingResp
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, fmt.Errorf("sliding sync decode: %w: %s", err, string(raw))
	}
	if sr.Pos == "" {
		return nil, fmt.Errorf("sliding sync: no pos: %s", string(raw))
	}
	c.slidingPos = sr.Pos

	var events []Event
	for rid, room := range sr.Rooms {
		for _, evRaw := range room.Timeline {
			ev := extractMessageEvent(rid, evRaw)
			if ev != nil {
				events = append(events, *ev)
			}
		}
	}
	return events, nil
}

type normalRoomTimeline struct {
	Events []json.RawMessage `json:"events"`
}

type normalRoom struct {
	Timeline normalRoomTimeline `json:"timeline"`
}

type normalRooms struct {
	Join map[string]normalRoom `json:"join,omitempty"`
}

type normalResp struct {
	NextBatch string      `json:"next_batch"`
	Rooms     normalRooms `json:"rooms,omitempty"`
}

func (c *Client) normalSync(timeout time.Duration) ([]Event, error) {
	u := c.baseURL + "/_matrix/client/v3/sync"
	q := url.Values{}
	if c.nextBatch != "" {
		q.Set("since", c.nextBatch)
	}
	if timeout > 0 {
		q.Set("timeout", strconv.Itoa(int(timeout.Milliseconds())))
	}
	q.Set("filter", `{"room":{"timeline":{"limit":10},"state":{"lazy_load_members":true}},"event_fields":["type","content","sender","event_id","origin_server_ts"]}`)
	u += "?" + q.Encode()

	resp, err := c.doRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var nr normalResp
	if err := json.Unmarshal(raw, &nr); err != nil {
		return nil, fmt.Errorf("sync decode: %w: %s", err, string(raw))
	}
	if nr.NextBatch == "" {
		return nil, fmt.Errorf("sync: no next_batch: %s", string(raw))
	}
	c.nextBatch = nr.NextBatch

	var events []Event
	for rid, room := range nr.Rooms.Join {
		for _, evRaw := range room.Timeline.Events {
			ev := extractMessageEvent(rid, evRaw)
			if ev != nil {
				events = append(events, *ev)
			}
		}
	}
	return events, nil
}

type rawContent struct {
	Body    string `json:"body,omitempty"`
	MsgType string `json:"msgtype,omitempty"`
}

type rawEvent struct {
	Type    string     `json:"type"`
	Sender  string     `json:"sender,omitempty"`
	Content rawContent `json:"content,omitempty"`
	EventID string     `json:"event_id,omitempty"`
	TS      int64      `json:"origin_server_ts,omitempty"`
}

func extractMessageEvent(roomID string, raw json.RawMessage) *Event {
	var ev rawEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil
	}
	if ev.Type != "m.room.message" {
		return nil
	}
	msg := Message{
		EventID:   ev.EventID,
		Sender:    ev.Sender,
		Body:      ev.Content.Body,
		MsgType:   ev.Content.MsgType,
		RoomID:    roomID,
		Timestamp: ev.TS,
	}
	data, _ := json.Marshal(msg)
	return &Event{Type: "m.room.message", Data: data}
}

func (c *Client) SendMessage(roomID, text string) error {
	txnID := atomic.AddInt64(&c.txnID, 1)
	u := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%d", c.baseURL, url.PathEscape(roomID), txnID)

	body, _ := json.Marshal(map[string]string{
		"msgtype": "m.text",
		"body":    text,
	})

	resp, err := c.doRequest(http.MethodPut, u, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("send: status %d: %s", resp.StatusCode, string(raw))
	}
	return nil
}

func (c *Client) setAuth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
}

func (c *Client) doRequest(method, fullURL string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	u := fullURL
	if len(u) > 0 && u[0] == '/' {
		u = c.baseURL + u
	}

	req, err := http.NewRequest(method, u, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.setAuth(req)

	return c.hc.Do(req)
}


