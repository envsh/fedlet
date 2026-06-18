package matrixlite

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const slidingSyncEndpoint = "/_matrix/client/unstable/org.matrix.simplified_msc3575/sync"

type Client struct {
	baseURL      string
	accessToken  string
	refreshToken string
	userID       string
	deviceID     string
	nextBatch    string
	slidingPos   string
	useSliding   bool
	txnID        int64
	hc           *http.Client
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

func ClientFromToken(baseURL, accessToken string) (*Client, error) {
	c := &Client{
		baseURL:     baseURL,
		accessToken: accessToken,
		hc: &http.Client{
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
		},
	}
	c.detectSlidingSync()
	if _, err := c.Sync(0); err != nil {
		return nil, fmt.Errorf("token validation: %w", err)
	}
	return c, nil
}

type loginReq struct {
	Type       string `json:"type"`
	User       string `json:"user"`
	Password   string `json:"password"`
	DeviceName string `json:"initial_device_display_name,omitempty"`
	RefreshTok bool   `json:"refresh_token,omitempty"`
}

type loginResp struct {
	UserID       string `json:"user_id"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	DeviceID     string `json:"device_id"`
}

func (c *Client) doLogin(user, password string) error {
	body, _ := json.Marshal(loginReq{
		Type:       "m.login.password",
		User:       user,
		Password:   password,
		DeviceName: "fedlet-bridge",
		RefreshTok: true,
	})
	loginURL := c.baseURL + "/_matrix/client/v3/login"
	resp, err := c.doRequest(http.MethodPost, loginURL, body)
	if err != nil {
		return fmt.Errorf("login %s: %w", loginURL, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	bodyStr := string(raw)
	if len(bodyStr) > 0 && bodyStr[0] == '<' {
		return fmt.Errorf(
			"login %s: server returned HTML — "+
				"the URL appears to be a web interface (like Element Web), "+
				"not a Matrix homeserver API endpoint\n"+
				"  Correct:  -matrix-url matrix.example.com\n"+
				"  Wrong:    -matrix-url chat.example.com", loginURL)
	}

	var lr loginResp
	if err := json.Unmarshal(raw, &lr); err != nil {
		bodySnippet := string(raw)
		if len(bodySnippet) > 500 {
			bodySnippet = bodySnippet[:500] + "..."
		}
		return fmt.Errorf("login decode %s: %w: %s", loginURL, err, bodySnippet)
	}
	if lr.AccessToken == "" {
		return fmt.Errorf("login: no access_token: %s", string(raw))
	}
	c.accessToken = lr.AccessToken
	c.refreshToken = lr.RefreshToken
	c.userID = lr.UserID
	c.deviceID = lr.DeviceID
	return nil
}

func (c *Client) RestoreFromState(s *State) {
	c.accessToken = s.AccessToken
	c.refreshToken = s.RefreshToken
	c.userID = s.UserID
	c.deviceID = s.DeviceID
	c.nextBatch = s.NextBatch
	c.slidingPos = s.SlidingPos
	c.useSliding = s.UseSliding
}

func (c *Client) SaveSyncState(s *State) {
	s.AccessToken = c.accessToken
	s.RefreshToken = c.refreshToken
	s.UserID = c.userID
	s.DeviceID = c.deviceID
	s.UseSliding = c.useSliding
	if c.useSliding {
		s.SlidingPos = c.slidingPos
	} else {
		s.NextBatch = c.nextBatch
	}
}

type refreshReq struct {
	RefreshToken string `json:"refresh_token"`
}

type refreshResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresInMS  int    `json:"expires_in_ms"`
}

func (c *Client) TokenRefresh() error {
	if c.refreshToken == "" {
		return fmt.Errorf("no refresh_token available")
	}
	body, _ := json.Marshal(refreshReq{RefreshToken: c.refreshToken})
	resp, err := c.doRequest(http.MethodPost, "/_matrix/client/v1/refresh", body)
	if err != nil {
		return fmt.Errorf("refresh: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var rr refreshResp
	if err := json.Unmarshal(raw, &rr); err != nil {
		return fmt.Errorf("refresh decode: %w: %s", err, string(raw))
	}
	if rr.AccessToken == "" {
		return fmt.Errorf("refresh: no access_token: %s", string(raw))
	}
	c.accessToken = rr.AccessToken
	if rr.RefreshToken != "" {
		c.refreshToken = rr.RefreshToken
	}
	return nil
}

type versionsResp struct {
	Versions         []string          `json:"versions"`
	UnstableFeatures map[string]bool `json:"unstable_features"`
}

func (c *Client) detectSlidingSync() {
	resp, err := c.doRequest(http.MethodGet, "/_matrix/client/v3/versions", nil)
	if err != nil {
		log.Printf("matrixlite: sliding sync detection failed: %v", err)
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if len(raw) > 0 && raw[0] == '<' {
		log.Printf("matrixlite: sliding sync: server returned HTML — check URL is a Matrix homeserver API endpoint")
		return
	}

	var vr versionsResp
	if err := json.Unmarshal(raw, &vr); err != nil {
		log.Printf("matrixlite: sliding sync: cannot parse versions response: %v", err)
		return
	}

	log.Printf("matrixlite: server spec versions: %s", strings.Join(vr.Versions, ", "))

	c.useSliding = vr.UnstableFeatures["org.matrix.simplified_msc3575"]
	if c.useSliding {
		log.Printf("matrixlite: server supports Sliding Sync (MSC4186)")
	} else {
		log.Printf("matrixlite: server does not support Sliding Sync, using normal sync")
	}
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

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrTokenExpired
	}

	var sr slidingResp
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, fmt.Errorf("sliding sync decode: %w: %s", err, string(raw))
	}
	if sr.Pos == "" {
		return nil, fmt.Errorf("sliding sync: no pos: %s", string(raw))
	}
	c.slidingPos = sr.Pos
	log.Printf("matrixlite: sliding sync pos -> %s", c.slidingPos)

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

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrTokenExpired
	}

	var nr normalResp
	if err := json.Unmarshal(raw, &nr); err != nil {
		return nil, fmt.Errorf("sync decode: %w: %s", err, string(raw))
	}
	if nr.NextBatch == "" {
		return nil, fmt.Errorf("sync: no next_batch: %s", string(raw))
	}
	c.nextBatch = nr.NextBatch
	log.Printf("matrixlite: normal sync since -> %s", c.nextBatch)
	diff := compactSyncDiff(prevNormalNextBatch, c.nextBatch)
	prevNormalNextBatch = c.nextBatch
	log.Printf("matrixlite: sync diff: %s", diff)

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

var (
	ErrWellKnownNotFound  = errors.New("well-known: not found")
	ErrWellKnownMalformed = errors.New("well-known: malformed response")
	ErrWellKnownNetwork   = errors.New("well-known: network error")
	ErrTokenExpired       = errors.New("matrixlite: token expired")

	wellKnownHC          *http.Client
	prevNormalNextBatch string
)

func compactSyncDiff(prev, curr string) string {
	labels := []string{"stream", "presence", "receipt", "account", "to_device", "device", "groups", "typing"}

	if prev == curr {
		p := strings.TrimPrefix(curr, "s")
		if idx := strings.Index(p, "_"); idx > 0 {
			p = p[:idx]
		}
		return "no changes (stream=" + p + ")"
	}

	// Synapse multi-column: sN_N_N...
	if strings.HasPrefix(curr, "s") && strings.Contains(curr, "_") {
		prevP := strings.Split(strings.TrimPrefix(prev, "s"), "_")
		currP := strings.Split(strings.TrimPrefix(curr, "s"), "_")

		var parts []string
		for i, l := range labels {
			if i >= len(prevP) || i >= len(currP) {
				break
			}
			pv, _ := strconv.ParseInt(prevP[i], 10, 64)
			cv, _ := strconv.ParseInt(currP[i], 10, 64)
			if pv != cv {
				parts = append(parts, fmt.Sprintf("%s %+d", l, cv-pv))
			}
		}
		if prev == "" {
			return "stream=" + currP[0]
		}
		return strings.Join(parts, "  ")
	}

	// sN or plain number
	label := "since"
	prevS := strings.TrimPrefix(prev, "s")
	currS := strings.TrimPrefix(curr, "s")
	if strings.HasPrefix(curr, "s") {
		label = "stream"
	}
	pv, _ := strconv.ParseInt(prevS, 10, 64)
	cv, _ := strconv.ParseInt(currS, 10, 64)
	if prev == "" || pv == 0 {
		return fmt.Sprintf("%s=%d", label, cv)
	}
	return fmt.Sprintf("%s %+d", label, cv-pv)
}

func getWellKnownHC() *http.Client {
	if wellKnownHC != nil {
		return wellKnownHC
	}
	return &http.Client{Timeout: 10 * time.Second}
}

type wellKnownResp struct {
	Homeserver struct {
		BaseURL string `json:"base_url"`
	} `json:"m.homeserver"`
}

func DiscoverBaseURL(input string) (string, error) {
	rawURL := input
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		rawURL = strings.TrimRight(rawURL, "/")
		return rawURL, fmt.Errorf("well-known: %w: invalid URL %q", ErrWellKnownMalformed, input)
	}
	if u.Host == "" {
		rawURL = strings.TrimRight(rawURL, "/")
		return rawURL, fmt.Errorf("well-known: %w: no host in %q", ErrWellKnownNotFound, input)
	}
	discovered, err := fetchWellKnown(u.Host)
	if err != nil {
		rawURL = strings.TrimRight(rawURL, "/")
		return rawURL, err
	}
	discovered = strings.TrimRight(discovered, "/")
	return discovered, nil
}

func fetchWellKnown(host string) (string, error) {
	u := "https://" + host + "/.well-known/matrix/client"
	hc := getWellKnownHC()
	resp, err := hc.Get(u)
	if err != nil {
		return "", fmt.Errorf("well-known: %w for %s: %w", ErrWellKnownNetwork, host, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return "", fmt.Errorf("well-known: %w: HTTP %d", ErrWellKnownNotFound, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("well-known: %w: HTTP %d for %s", ErrWellKnownNotFound, resp.StatusCode, host)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("well-known: %w: read: %w", ErrWellKnownNetwork, err)
	}

	var wk wellKnownResp
	if err := json.Unmarshal(raw, &wk); err != nil {
		return "", fmt.Errorf("well-known: %w: decode: %w", ErrWellKnownMalformed, err)
	}
	if wk.Homeserver.BaseURL == "" {
		return "", fmt.Errorf("well-known: %w: empty m.homeserver.base_url", ErrWellKnownMalformed)
	}
	return wk.Homeserver.BaseURL, nil
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

type whoamiResp struct {
	UserID string `json:"user_id"`
}

func (c *Client) whoami() (string, error) {
	resp, err := c.doRequest(http.MethodGet, "/_matrix/client/v3/account/whoami", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", ErrTokenExpired
	}

	var wr whoamiResp
	if err := json.Unmarshal(raw, &wr); err != nil {
		return "", fmt.Errorf("whoami decode: %w: %s", err, string(raw))
	}
	if wr.UserID == "" {
		return "", fmt.Errorf("whoami: empty user_id: %s", string(raw))
	}
	return wr.UserID, nil
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
		return nil, fmt.Errorf("%s %s: %w", method, u, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.setAuth(req)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, req.URL.String(), err)
	}
	return resp, nil
}


