package matrixlite

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

var (
	pubfn_    func(any) error
	muClient  sync.Mutex
	curClient *Client
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

func parseAuth(auth string) (token, user, password string) {
	if auth == "" {
		return "", "", ""
	}
	if !strings.Contains(auth, ":") {
		return auth, "", ""
	}
	for i := len(auth) - 1; i >= 0; i-- {
		if auth[i] == ':' {
			return "", auth[:i], auth[i+1:]
		}
	}
	return "", "", ""
}

func Start(server, auth string) {
	baseURL, err := DiscoverBaseURL(server)
	if err != nil {
		switch {
		case errors.Is(err, ErrWellKnownNotFound):
			log.Printf("matrixlite: no well-known for %s, using as-is (if you see HTML errors, this may be a web UI like Element Web; use the homeserver domain instead)", server)
		default:
			log.Printf("matrixlite: well-known discovery for %s: %v; using as-is", server, err)
		}
	}
	log.Printf("matrixlite: using base URL %s (from %s)", baseURL, server)
	token, user, password := parseAuth(auth)
	go pollLoop(baseURL, token, user, password)
}

func pollLoop(baseURL, token, user, password string) {
	statusRunning.Store(true)
	statusConnectedSince.Store(time.Now())
	defer statusRunning.Store(false)

	log.Printf("matrixlite: server=%s user=%s", baseURL, user)

	// One worker for the whole process; room summary lookups must never run on
	// the publish path.
	StartBackfillWorker()

	// Retry sweep: rooms whose first backfill failed get re-queued once their
	// cooldown lapses, without needing a fresh message to trigger them.
	go func() {
		t := time.NewTicker(roomProfileCooldown / 2)
		defer t.Stop()
		for range t.C {
			muClient.Lock()
			c := curClient
			muClient.Unlock()
			if c != nil {
				sweepJoinedRooms(c)
			}
		}
	}()

	var state State
	state.Load()

	for {
		// ═══ RESTORE ═══
		client, err := loginOrRestore(baseURL, token, user, password, &state)
		switch {
		case errors.Is(err, ErrUserDeactivated):
			pushError(err)
			log.Printf("matrixlite: user deactivated, stopping")
			return
		case err != nil:
			pushError(err)
			log.Printf("matrixlite: %v, retrying in 5s", err)
			time.Sleep(5 * time.Second)
			continue
		case client == nil:
			time.Sleep(10 * time.Second)
			continue
		}

		// One probe per (re)connection covers restore, token-refresh and
		// password-login paths alike, and re-detects after server upgrades.
		client.detectSummarySupport()
		// Sweep once per connection so every joined room gets its profile even
		// if it never carries a message after restart.
		go sweepJoinedRooms(client)

		muClient.Lock()
		curClient = client
		muClient.Unlock()

		state.Server = baseURL
		state.LoginToken = token
		client.SaveSyncState(&state)
		state.Save()

		// ═══ SYNC ═══
		var lastErr error
		for {
			ms, err := client.Sync(30 * time.Second)
			if err == nil {
				client.SaveSyncState(&state)
				state.Save()
				for _, m := range ms {
					rid, _ := m["room_id"].(string)
					if rid != "" {
						log.Printf("matrixlite: event in room %s, msgtype %s", rid, rawEventMsgtype(m))
					}
					w, ok := RoomProfileForPublish(rid)
					if !ok {
						// room_profile is always published so subscribers can rely
						// on the key existing; when we know nothing yet it ships as
						// an empty object and the backfill path stays re-queued
						// until the room has something publishable.
						EnqueueBackfill(rid)
					}
					m["room_profile"] = w
					// The member name is the attribute that matters, so it takes
					// priority: presence rides along only when the server sends
					// it. The key is omitted entirely when we know nothing,
					// rather than written as an empty object.
					if sender, _ := m["sender"].(string); sender != "" {
						if sp, ok := MemberProfileForPublish(sender); ok {
							m["sender_profile"] = sp
						}
						if !memberHasIdentity(sender) {
							EnqueueMember(sender, rid)
						}
					}
					data, _ := json.Marshal(m)
					if err := publish(m); err != nil {
						log.Printf("matrixlite: publish error: %v", err)
					}
					um, ok := matrixEventToUnified(m, data)
					if ok {
						publish(um)
					}
				}
				continue
			}

			lastErr = err
			pushError(err)
			switch {
			case errors.Is(err, ErrUserDeactivated):
				pushError(err)
				log.Printf("matrixlite: sync: user deactivated, stopping")
				return

			case errors.Is(err, ErrTokenExpired):
				if client.refreshToken == "" {
					break // → RECONNECT
				}
				switch rerr := client.TokenRefresh(); {
				case rerr == nil:
					log.Printf("matrixlite: sync: token refreshed")
					client.SaveSyncState(&state)
					state.Save()
					continue
				case errors.Is(rerr, ErrUserDeactivated):
					pushError(rerr)
					return
				case errors.Is(rerr, ErrTokenExpired),
					errors.Is(rerr, ErrSessionInvalidated):
					break // → RECONNECT
				default:
					pushError(rerr)
					log.Printf("matrixlite: sync: refresh transient error, retrying sync: %v", rerr)
					time.Sleep(5 * time.Second)
					continue
				}

			case errors.Is(err, ErrSessionInvalidated):
				if client.refreshToken != "" {
					switch rerr := client.TokenRefresh(); {
					case rerr == nil:
						log.Printf("matrixlite: sync: token refreshed after session error")
						client.SaveSyncState(&state)
						state.Save()
						continue
					case errors.Is(rerr, ErrUserDeactivated):
						pushError(rerr)
						return
					case errors.Is(rerr, ErrTokenExpired),
						errors.Is(rerr, ErrSessionInvalidated):
						break // → RECONNECT
					default:
						pushError(rerr)
						log.Printf("matrixlite: sync: refresh transient error, retrying sync: %v", rerr)
						time.Sleep(5 * time.Second)
						continue
					}
				}
				break // → RECONNECT

			default:
				pushError(err)
				log.Printf("matrixlite: sync error (transient): %v, retrying in 5s", err)
				time.Sleep(5 * time.Second)
				continue
			}

			break
		}

		// ═══ RECONNECT ═══
		log.Printf("matrixlite: session lost, reconnecting in 5s (reason: %v)", lastErr)
		time.Sleep(5 * time.Second)
	}
}

func loginOrRestore(baseURL, token, user, password string, state *State) (*Client, error) {
	c := &Client{
		baseURL: baseURL,
		hc: &http.Client{
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
		},
		summaryClient: &http.Client{Timeout: summaryTimeout},
	}

	if token == "" && state.LoginToken != "" {
		token = state.LoginToken
	}

	if state.Valid() && state.Server == baseURL {
		c.RestoreFromState(state)
		whoms, whoErr := c.whoami()
		if whoErr == nil && state.UserID != "" && whoms != state.UserID {
			log.Printf("matrixlite: [cache] account switched: %s → %s", state.UserID, whoms)
			state.AccessToken = ""
			state.RefreshToken = ""
			state.Save()
		}

		if whoErr == nil {
			log.Printf("matrixlite: [cache] restored session for %s (sliding=%v)", c.userID, c.useSliding)
			return c, nil
		}

		log.Printf("matrixlite: [cache] whoami failed: %v", whoErr)
		if c.refreshToken != "" && (errors.Is(whoErr, ErrTokenExpired) || errors.Is(whoErr, ErrSessionInvalidated)) {
			if rerr := c.TokenRefresh(); rerr == nil {
				log.Printf("matrixlite: [cache] token refreshed for %s", c.userID)
				c.SaveSyncState(state)
				state.Save()
				if _, err := c.whoami(); err == nil {
					return c, nil
				}
			} else if errors.Is(rerr, ErrUserDeactivated) {
				return nil, ErrUserDeactivated
			}
		}

		if errors.Is(whoErr, ErrUserDeactivated) {
			return nil, ErrUserDeactivated
		}

		if errors.Is(whoErr, ErrTokenExpired) || errors.Is(whoErr, ErrSessionInvalidated) {
			state.AccessToken = ""
			state.RefreshToken = ""
			state.Save()
			log.Printf("matrixlite: [cache] session expired, re-logging in")
		} else {
			return nil, fmt.Errorf("whoami: %w", whoErr)
		}
	}

	if token != "" {
		client, err := ClientFromToken(baseURL, token)
		if err != nil {
			log.Printf("matrixlite: [auth] token login error: %v", err)
			return nil, err
		}
		log.Printf("matrixlite: [auth] logged in with token (sliding=%v)", client.useSliding)
		return client, nil
	}

	var err error
	if state.DeviceID != "" {
		c, err = LoginWithDeviceID(baseURL, user, password, state.DeviceID)
	} else {
		c, err = Login(baseURL, user, password)
	}
	if err != nil {
		log.Printf("matrixlite: [auth] login error: %v", err)
		return nil, err
	}
	log.Printf("matrixlite: [auth] logged in as %s (sliding=%v)", c.userID, c.useSliding)
	return c, nil
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

func IsRunning() bool { return statusRunning.Load() }
func ConnectedSince() time.Time {
	v := statusConnectedSince.Load()
	if v == nil {
		return time.Time{}
	}
	return v.(time.Time)
}
func ReconnTimes() int64 { return statusReconnTimes.Load() }
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

func Send(roomID, msg, msgType string, filedata []byte, fileinfo *fbshared.MediaDataInfo, extra *fbshared.SendExtra) (fbshared.SendResult, error) {
	_ = extra
	if roomID == "" || msg == "" {
		return fbshared.SendResult{}, fmt.Errorf("matrixlite: empty roomID or message")
	}
	muClient.Lock()
	c := curClient
	muClient.Unlock()
	if c == nil {
		return fbshared.SendResult{}, fmt.Errorf("matrixlite: no active session")
	}
	if len(filedata) > 0 {
		if fileinfo == nil {
			return fbshared.SendResult{}, fmt.Errorf("matrixlite: filedata present but fileinfo is nil")
		}
		return fbshared.SendResult{}, c.SendFileMessage(roomID, filedata, fileinfo)
	}
	return fbshared.SendResult{}, c.SendMessage(roomID, msg)
}

func DownloadMedia(mxcURL string) (io.ReadCloser, string, error) {
	muClient.Lock()
	c := curClient
	muClient.Unlock()
	if c == nil {
		return nil, "", fmt.Errorf("matrixlite: not connected")
	}
	return c.downloadMedia(mxcURL)
}

func Redact(roomID, eventID, reason string) (fbshared.SendResult, error) {
	if roomID == "" || eventID == "" {
		return fbshared.SendResult{}, fmt.Errorf("matrixlite: empty roomID or eventID")
	}
	muClient.Lock()
	c := curClient
	muClient.Unlock()
	if c == nil {
		return fbshared.SendResult{}, fmt.Errorf("matrixlite: no active session")
	}
	return c.RedactMessage(roomID, eventID, reason)
}

// RoomProfileForPublish returns the publishable wire form of a room, mirroring
// MemberProfileForPublish. The false return is what keeps room_profile from
// ever being written as an empty object: a room we know nothing about — or one
// whose last backfill came back empty or errored — gets no key at all and
// stays eligible for another backfill.
func RoomProfileForPublish(roomID string) (fbshared.RoomProfileWire, bool) {
	p := RoomProfile(roomID)
	if p == nil || !p.hasVisibleData() {
		return fbshared.RoomProfileWire{}, false
	}
	return p.wire(), true
}

func matrixEventToUnified(m map[string]any, raw []byte) (fbshared.UnifiedMessage, bool) {
	um := fbshared.UnifiedMessage{
		Protocol:  fbshared.ProtoMatrixLite,
		MsgType:   fbshared.MsgTypeCreate,
		MsgFormat: fbshared.FmtText,
		Timestamp: time.Now().UnixNano(),
		Raw:       raw,
	}

	if s, _ := m["event_id"].(string); s != "" {
		um.MsgID = s
	}
	if s, _ := m["sender"].(string); s != "" {
		um.UserID = s
		um.Username = s
	}
	if s, _ := m["room_id"].(string); s != "" {
		um.ChatID = s
	}
	// Reuse the exact snapshot already attached to the raw event, so both
	// publish paths report identical metadata.
	if w, ok := m["room_profile"].(fbshared.RoomProfileWire); ok {
		um.ApplyRoomProfile(w)
	}
	// Reuse the exact snapshot already attached to the raw event, so both
	// publish paths report identical member attributes.
	if w, ok := m["sender_profile"].(fbshared.MemberProfileWire); ok {
		um.ApplySenderProfile(w)
	}
	if f, ok := m["origin_server_ts"].(float64); ok && f > 0 {
		um.Timestamp = int64(f) * 1000000
	}

	if content, ok := m["content"].(map[string]any); ok {
		if s, _ := content["body"].(string); s != "" {
			um.Text = s
		}
		if s, _ := content["msgtype"].(string); s != "" {
			um.MsgType = s
		}
		if s, _ := content["format"].(string); s == "org.matrix.custom.html" {
			if s2, _ := content["formatted_body"].(string); s2 != "" {
				um.HTML = s2
				um.MsgFormat = fbshared.FmtHTML
			}
		}
	}

	if um.Text == "" {
		return um, false
	}
	return um, true
}

func rawEventMsgtype(m map[string]any) string {
	content, _ := m["content"].(map[string]any)
	mt, _ := content["msgtype"].(string)
	return mt
}
