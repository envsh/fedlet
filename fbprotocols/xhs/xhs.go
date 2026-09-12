package xhs

// Protocol orchestrator: Start / poll loop / dedupe state / throttle / status.
//
// Behaviour:
//   - hot board: guest session is fine (login/activate), ~600s, publishes only
//     newly-appeared entries
//   - notifications: require a real login, ~60s, new events only
//   - session healing: periodic verify + passive auth-error detection; on a
//     lost session the feeds pause and the single ensureSession gateway
//     restarts the login UI (cooldown-gated; resets once the session verifies OK)
//   - risk control: 461/471/472 responses hand control to captcha.go via the
//     client risk interceptor; rounds are throttled by the rate gate
//
// State (dedupe, 72h window) persists to ~/.config/fedlet/xhs-state.json.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultHotInterval    = 600 * time.Second
	defaultNotifyInterval = 60 * time.Second
	authCheckInterval     = 30 * time.Minute
	dedupeExpiry          = 72 * time.Hour
	reLoginCooldown       = 5 * time.Minute
)

var (
	pubfn_    func(any) error
	muClient  sync.Mutex
	hotOn     bool
	notifyOn  bool
	hotInt    time.Duration
	notifyInt time.Duration
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

// xhsState is the persisted dedupe set: note ids / notification ids seen by
// consent to a seen-timestamp map (72h window).
type xhsState struct {
	Hotlist       map[string]int64 `json:"hotlist"`
	Notifications map[string]int64 `json:"notifications"`
}

func newState() *xhsState {
	s := &xhsState{}
	if s.Hotlist == nil {
		s.Hotlist = map[string]int64{}
	}
	if s.Notifications == nil {
		s.Notifications = map[string]int64{}
	}
	return s
}

// Start launches the poll loop. hot/notify enable the two feeds; the intervals
// are 0-for-default (600s / 60s).
func Start(hot, notify bool, hotInterval, notifyInterval time.Duration) {
	if hotInterval <= 0 {
		hotInterval = defaultHotInterval
	}
	if notifyInterval <= 0 {
		notifyInterval = defaultNotifyInterval
	}
	muClient.Lock()
	hotOn = hot
	notifyOn = notify
	hotInt = hotInterval
	notifyInt = notifyInterval
	muClient.Unlock()
	go pollLoop()
}

func pollLoop() {
	statusRunning.Store(true)
	statusConnectedSince.Store(time.Now())
	defer statusRunning.Store(false)

	muClient.Lock()
	hot := hotOn
	notify := notifyOn
	hi := hotInt
	ni := notifyInt
	muClient.Unlock()
	logPrefix("hot=%v notify=%v intervals=%s/%s", hot, notify, hi, ni)

	loadAuthInto(client())
	state := loadState(stateFilePath())

	if !hot && !notify {
		logPrefix("both feeds disabled, nothing to poll")
		return
	}
	// guest-first: the hot feed only needs an anonymous session, so mint one
	// before deciding on the login gateway — the login UI only pops when a real
	// session is actually required.
	if hot && AuthStatus() == AuthStatusEmpty {
		ensureGuestSession()
	}
	ensureSession(time.Now(), true)

	now := time.Now()
	nextHot := now.Add(hi)
	nextNotify := now.Add(ni)
	nextAuth := now.Add(authCheckInterval)

	tick := ni
	if !notify {
		tick = hi
	}
	if tick > time.Minute {
		tick = time.Minute
	}

	for {
		now = time.Now()

		if reloginDue(now) {
			if s := AuthStatus(); s != AuthStatusReady && s != authStatusGuest {
				ensureSession(now, false)
			}
		}
		if hot && now.After(nextHot) {
			nextHot = now.Add(hi)
			if feedAllowed(AuthStatus(), false) {
				hotRound(state)
			}
		}
		if notify && now.After(nextNotify) {
			nextNotify = now.Add(ni)
			if feedAllowed(AuthStatus(), true) {
				notifyRound(state)
			}
		}
		if notify && now.After(nextAuth) {
			ensureSession(time.Now(), false)
			nextAuth = now.Add(authCheckInterval)
		}

		pruneState(state, time.Now())
		saveState(stateFilePath(), state)
		time.Sleep(tick)
	}
}

// feedAllowed gates a feed on the auth status: notifications require a real
// login; the hot board also runs with a guest (anonymous) session.
func feedAllowed(status string, needLogin bool) bool {
	if needLogin {
		return status == AuthStatusReady
	}
	return status == AuthStatusReady || status == authStatusGuest
}

// reloginDue reports whether a scheduled re-login attempt is due (zero cooldown
// or elapsed), letting pollLoop retry the login gateway on its own cadence with
// the feeds gated off meanwhile.
func reloginDue(now time.Time) bool {
	reLoginMu.Lock()
	defer reLoginMu.Unlock()
	return reLogin.nextAt.IsZero() || !now.Before(reLogin.nextAt)
}

func hotRound(state *xhsState) {
	waitRateGate()
	resp, err := FetchHotlist()
	if err != nil {
		logPrefix("hotlist error: %v", err)
		handleRoundErr(state, err)
		return
	}
	now := time.Now()
	published := 0
	for i := range resp.Data {
		it := &resp.Data[i]
		key := it.NoteID
		if key == "" {
			continue
		}
		if _, seen := state.Hotlist[key]; seen {
			continue
		}
		state.Hotlist[key] = now.Unix()
		title := HotlistTitle(it)
		detail := HotlistDetail(it)
		logPrefix("hotlist #%d %s %s", i+1, key, truncate(title, 80))
		payload := map[string]any{
			"kind":         "hotlist",
			"rank":         it.Rank,
			"feed_id":      it.ID,
			"type":         it.Type,
			"title":        title,
			"detail":       detail,
			"url":          HotlistLink(it),
			"note_id":      it.NoteID,
			"count":        len(resp.Data),
			"published_at": now.Unix(),
		}
		if err := publish(payload); err != nil {
			logPrefix("publish hotlist %s error: %v", key, err)
		}
		published++
	}
	if published > 0 {
		logPrefix("hotlist round published %d new entries", published)
	} else {
		logPrefix("hotlist round no change")
	}
}

func notifyRound(state *xhsState) {
	waitRateGate()
	resp, err := FetchNotifications()
	if err != nil {
		logPrefix("notifications error: %v", err)
		handleRoundErr(state, err)
		return
	}
	now := time.Now()
	published := 0
	for i := range resp.Items {
		it := &resp.Items[i]
		key := string(it.Kind) + ":" + it.ID
		if it.ID == "" {
			continue
		}
		if _, seen := state.Notifications[key]; seen {
			continue
		}
		state.Notifications[key] = now.Unix()
		logPrefix("notify %s actor=%s %s", it.Kind, it.ActorName, truncate(it.Text, 80))
		payload := map[string]any{
			"kind":         "notification",
			"notify_type":  string(it.Kind),
			"id":           it.ID,
			"actor":        it.ActorName,
			"actor_id":     it.ActorID,
			"text":         it.Text,
			"note_id":      it.NoteID,
			"note_title":   it.NoteTitle,
			"is_unread":    it.Unread,
			"created_time": it.CreateTime,
			"unread_total": resp.Unread,
			"published_at": now.Unix(),
		}
		if err := publish(payload); err != nil {
			logPrefix("publish notification %s error: %v", key, err)
		}
		published++
	}
	if published > 0 {
		logPrefix("notifications round published %d new events", published)
	} else {
		logPrefix("notifications round no change")
	}
}

// handleRoundErr routes a feed-round failure: session errors pause the feeds and
// go through the login gateway; everything else is surfaced as a status error.
func handleRoundErr(state *xhsState, err error) {
	if isSessionErr(err) {
		logPrefix("session lost, feeds paused pending re-login")
		ensureSession(time.Now(), false)
		return
	}
	pushError(err)
}

var (
	probeSession   = verifySession
	startLoginUIFn = startLoginUI
)

// ensureSession is the only entry point that may start the login UI. A ready
// session is validated; a missing or expired one schedules a re-login under a
// cooldown that resets once the session verifies OK again. Transient verify
// failures (network / 5xx) are kept as-is: they never touch the cooldown or pop
// the login window.
func ensureSession(now time.Time, force bool) {
	status := AuthStatus()
	if status == AuthStatusReady || status == authStatusGuest {
		if err := probeSession(); err == nil {
			reLoginMu.Lock()
			reLogin.nextAt = time.Time{}
			reLoginMu.Unlock()
			return
		} else if !errors.Is(err, ErrNotLoggedIn) {
			logPrefix("session check transient error, session kept: %v", err)
			pushError(err)
			return
		}
		logPrefix("session check failed, scheduling re-login")
	}
	scheduleLogin(now, force)
}

func scheduleLogin(now time.Time, force bool) {
	reLoginMu.Lock()
	if !force && now.Before(reLogin.nextAt) {
		reLoginMu.Unlock()
		logPrefix("re-login in cooldown until %s", reLogin.nextAt.Format("15:04:05"))
		return
	}
	reLogin.nextAt = now.Add(reLoginCooldown)
	reLoginMu.Unlock()

	url, err := startLoginUIFn()
	if err != nil {
		logPrefix("start login UI: %v", err)
		return
	}
	openLoginBrowserOnce(url)
	logPrefix("login UI available at %s", url)
}

// reLoginMu guards the auto re-login cooldown (not to be confused with the
// client's authState mutex in client.go or auth.go's authMu). The UI is not
// re-opened before reLogin.nextAt; a successful session verify clears it.
var (
	reLoginMu sync.Mutex
	reLogin   = struct {
		nextAt time.Time
	}{}
)

func isSessionErr(err error) bool {
	return errors.Is(err, ErrNotLoggedIn) || isAuthRejectedText(err)
}

// ---- rate gate (mirrors bdtieba: 4s -> 8s -> 16s -> 20s cap) ----

const (
	rateBase = 4 * time.Second
	rateMult = 2
	rateMax  = 20 * time.Second
)

var (
	rateMu      sync.Mutex
	rateUntil   time.Time
	rateBackoff time.Duration
)

func waitRateGate() {
	rateMu.Lock()
	for {
		if rateUntil.IsZero() || !rateUntil.After(time.Now()) {
			break
		}
		remain := time.Until(rateUntil)
		rateMu.Unlock()
		time.Sleep(remain)
		rateMu.Lock()
	}
	rateMu.Unlock()
}

func noteRateLimit() {
	rateMu.Lock()
	defer rateMu.Unlock()
	if rateBackoff == 0 {
		rateBackoff = rateBase
	} else {
		rateBackoff *= rateMult
		if rateBackoff > rateMax {
			rateBackoff = rateMax
		}
	}
	rateUntil = time.Now().Add(rateBackoff)
	logPrefix("risk control, throttling %s", rateBackoff)
}

func clearRateLimit() {
	rateMu.Lock()
	defer rateMu.Unlock()
	if rateUntil.After(time.Now()) {
		return
	}
	rateUntil = time.Time{}
	rateBackoff = rateBase
}

// ---- dedupe state persistence ----

func stateFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "fedlet", "xhs-state.json")
}

func loadState(path string) *xhsState {
	data, err := os.ReadFile(path)
	if err != nil {
		return newState()
	}
	var s xhsState
	if err := json.Unmarshal(data, &s); err != nil {
		logPrefix("load state parse error: %v", err)
		return newState()
	}
	if s.Hotlist == nil {
		s.Hotlist = map[string]int64{}
	}
	if s.Notifications == nil {
		s.Notifications = map[string]int64{}
	}
	return &s
}

func saveState(path string, s *xhsState) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		logPrefix("save state marshal error: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		logPrefix("save state mkdir error: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		logPrefix("save state write error: %v", err)
	}
}

func pruneState(s *xhsState, now time.Time) {
	cutoff := now.Add(-dedupeExpiry).Unix()
	prune := func(m map[string]int64) {
		for k, seen := range m {
			if seen < cutoff {
				delete(m, k)
			}
		}
	}
	prune(s.Hotlist)
	prune(s.Notifications)
}

// ---- protocol status ----

var (
	statusRunning        atomic.Bool
	statusConnectedSince atomic.Value
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

// ---- small shared helpers ----

// hc is the plain HTTP client for endpoints outside the signed flow
// (captcha register, QR images), shared to reuse connection pooling.
var hc = &http.Client{Timeout: 20 * time.Second}

// logPrefix prefixes xhs protocol log lines.
func logPrefix(format string, args ...any) {
	log.Printf("xhs: "+format, args...)
}

// decodeJSON decodes a body into a generic map (captcha endpoints etc).
func decodeJSON(r io.Reader) (map[string]any, error) {
	var v map[string]any
	dec := json.NewDecoder(r)
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// stringOut flattens any value for error messages.
func stringOut(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
