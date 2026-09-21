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
// Publish: each entry forwarded verbatim + flat proto_type/cycle_count (top level).

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

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

const (
	defaultHotInterval     = 600 * time.Second
	defaultNotifyInterval  = 60 * time.Second
	defaultCollectInterval = 1800 * time.Second
	authCheckInterval      = 30 * time.Minute
	dedupeExpiry           = 72 * time.Hour
	reLoginCooldown        = 5 * time.Minute
)

var (
	pubfn_     func(any) error
	muClient   sync.Mutex
	hotOn      bool
	notifyOn   bool
	collectOn  bool
	hotInt     time.Duration
	notifyInt  time.Duration
	collectInt time.Duration
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
	Collect       map[string]int64 `json:"collect"`
}

func newState() *xhsState {
	s := &xhsState{}
	if s.Hotlist == nil {
		s.Hotlist = map[string]int64{}
	}
	if s.Notifications == nil {
		s.Notifications = map[string]int64{}
	}
	if s.Collect == nil {
		s.Collect = map[string]int64{}
	}
	return s
}

// Start launches the poll loop. hot/notify enable the two feeds; the intervals
// are 0-for-default (600s / 60s). The collect feed (own favorites) requires a
// real login and only runs alongside notify — a pure-hot run is fully anonymous.
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
	collectOn = notify
	hotInt = hotInterval
	notifyInt = notifyInterval
	collectInt = defaultCollectInterval
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
	collect := collectOn
	hi := hotInt
	ni := notifyInt
	ci := collectInt
	muClient.Unlock()
	logPrefix("hot=%v notify=%v collect=%v intervals=%s/%s/%s", hot, notify, collect, hi, ni, ci)

	loadAuthInto(client())
	state := loadState(stateFilePath())

	if !hot && !notify {
		logPrefix("both feeds disabled, nothing to poll")
		return
	}
	ensureSession(time.Now(), true)

	now := time.Now()
	nextHot := now.Add(hi)
	nextNotify := now.Add(ni)
	nextCollect := now.Add(ci)
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
		if collect && now.After(nextCollect) {
			nextCollect = now.Add(ci)
			if feedAllowed(AuthStatus(), true) {
				collectRound(state)
			}
		}
		if (notify || collect) && now.After(nextAuth) {
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
		itemB, _ := json.Marshal(it.Raw)
		raw, aerr := fbshared.InsertFlatFields(itemB, map[string]any{
			"proto_type":  payload["kind"],
			"cycle_count": len(resp.Data),
		})
		if aerr != nil {
			logPrefix("publish hotlist %s error: %v", key, aerr)
		} else if err := publish(raw); err != nil {
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
		itemB, _ := json.Marshal(it.Raw)
		raw, aerr := fbshared.InsertFlatFields(itemB, map[string]any{
			"proto_type":  payload["kind"],
			"cycle_count": len(resp.Items),
		})
		if aerr != nil {
			logPrefix("publish notification %s error: %v", key, aerr)
		} else if err := publish(raw); err != nil {
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
	ensureGuest    = ensureGuestSession
)

// realLoginNeeded reports whether any running feed requires a real logged-in
// session (notifications / own collect); the hot board only needs a guest one,
// so a pure-hot run is fully anonymous.
func realLoginNeeded() bool {
	muClient.Lock()
	defer muClient.Unlock()
	return notifyOn || collectOn
}

// ensureSession is the only entry point that may start the login UI. A ready
// session is validated; a missing or expired one schedules a re-login under a
// cooldown that resets once the session verifies OK again. Transient verify
// failures (network / 5xx) are kept as-is: they never touch the cooldown or pop
// the login window. A guest session satisfies the anonymous hot board but is
// NOT enough when a feed requires a real login: the UI is opened then.
func ensureSession(now time.Time, force bool) {
	need := realLoginNeeded()
	switch status := AuthStatus(); status {
	case AuthStatusReady:
		if err := probeSession(); err == nil {
			if need && AuthStatus() == authStatusGuest {
				// probe downgraded ready -> guest (the stored session is an
				// anonymous one): notify/collect still need a real login, so
				// open the login UI instead of returning silently.
				logPrefix("session is guest, real login required, opening login UI")
				scheduleLogin(now, force)
				return
			}
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
		scheduleLogin(now, force)
	case authStatusGuest:
		if !need {
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
			ensureGuest() // dead guest: mint a fresh anonymous one, no login UI
			return
		}
		// guest suffices for the hot board but not for notify/collect: open the
		// login UI even though the guest session itself is valid.
		scheduleLogin(now, force)
	default: // AuthStatusEmpty / AuthStatusInvalid
		if !need {
			ensureGuest()
			return
		}
		scheduleLogin(now, force)
	}
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
	if s.Collect == nil {
		s.Collect = map[string]int64{}
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
	prune(s.Collect)
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
