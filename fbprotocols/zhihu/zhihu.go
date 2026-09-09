package zhihu

// Protocol orchestrator: Start / poll loop / dedupe state / throttle / status.
//
// Behaviour (first stage):
//   - hot list: requires the main z_c0 session (anonymous calls answer
//     101 AuthenticationError since 2026-09), ~600s, publishes only
//     newly-appeared entries
//   - notifications: also requires the z_c0 session, ~60s, new events only
//   - session healing: periodic /api/v4/me probe + passive 401 detection;
//     on a lost session the feeds pause and the single ensureSession gateway
//     restarts the login UI (bounded cooldown + attempt limit)
//
// State (dedupe, 72h window) persists to ~/.config/fedlet/zhihu-state.json.

import (
	"encoding/json"
	"errors"
	"log"
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
	reLoginMaxAttempts    = 2
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

// zhihuState is the persisted dedupe set: feed ids / notification ids seen by
// consent to a seen-timestamp map (72h window).
type zhihuState struct {
	Hotlist       map[string]int64 `json:"hotlist"`
	Notifications map[string]int64 `json:"notifications"`
}

func newState() *zhihuState {
	s := &zhihuState{}
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
	log.Printf("zhihu: hot=%v notify=%v intervals=%s/%s", hot, notify, hi, ni)

	loadAuth()
	state := loadState(stateFilePath())

	// Both feeds require the main z_c0 session (verified 2026-09: the hot list
	// answers 101 AuthenticationError anonymously, see RSSHub PR #19075).
	if !hot && !notify {
		log.Printf("zhihu: both feeds disabled, nothing to poll")
		return
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

		if hot && now.After(nextHot) {
			hotRound(state)
			nextHot = now.Add(hi)
		}
		if notify && now.After(nextNotify) {
			notifyRound(state)
			nextNotify = now.Add(ni)
		}
		if notify && now.After(nextAuth) {
			authCheck(state)
			nextAuth = now.Add(authCheckInterval)
		}

		pruneState(state, time.Now())
		saveState(stateFilePath(), state)
		time.Sleep(tick)
	}
}

func hotRound(state *zhihuState) {
	resp, err := FetchHotlist()
	if err != nil {
		log.Printf("zhihu: hotlist error: %v", err)
		handleRoundErr(state, err)
		return
	}
	now := time.Now()
	published := 0
	for i := range resp.Data {
		it := &resp.Data[i]
		if _, seen := state.Hotlist[it.ID]; seen {
			continue
		}
		state.Hotlist[it.ID] = now.Unix()
		title := HotlistTitle(it.Target)
		detail := HotlistDetail(it.Target)
		log.Printf("zhihu: hotlist #%d %s %s", i+1, it.ID, truncate(title, 80))
		payload := map[string]any{
			"kind":         "hotlist",
			"rank":         i,
			"feed_id":      it.ID,
			"type":         it.Type,
			"title":        title,
			"detail":       detail,
			"url":          HotlistLink(it.Target),
			"target":       it.Target,
			"fresh_text":   resp.FreshText,
			"count":        len(resp.Data),
			"published_at": now.Unix(),
		}
		if err := publish(payload); err != nil {
			log.Printf("zhihu: publish hotlist %s error: %v", it.ID, err)
		}
		published++
	}
	if published > 0 {
		log.Printf("zhihu: hotlist round published %d new entries", published)
	} else {
		log.Printf("zhihu: hotlist round no change")
	}
}

func notifyRound(state *zhihuState) {
	resp, err := FetchNotifications()
	if err != nil {
		log.Printf("zhihu: notifications error: %v", err)
		handleRoundErr(state, err)
		return
	}
	now := time.Now()
	published := 0
	for i := range resp.Data {
		it := &resp.Data[i]
		key := stringIt(it.ID)
		if _, seen := state.Notifications[key]; seen {
			continue
		}
		state.Notifications[key] = now.Unix()
		actor := ActorName(it.Actor)
		log.Printf("zhihu: notify id=%d actor=%s verb=%s %s",
			it.ID, actor, it.Verb, truncate(it.ActionText, 80))
		payload := map[string]any{
			"kind":         "notification",
			"id":           it.ID,
			"verb":         it.Verb,
			"action_text":  it.ActionText,
			"actor":        it.Actor,
			"target":       it.Target,
			"type":         it.Type,
			"is_read":      it.IsRead,
			"created_time": it.CreatedTime,
			"published_at": now.Unix(),
		}
		if err := publish(payload); err != nil {
			log.Printf("zhihu: publish notification %d error: %v", it.ID, err)
		}
		published++
	}
	if published > 0 {
		log.Printf("zhihu: notifications round published %d new events", published)
	} else {
		log.Printf("zhihu: notifications round no change")
	}
}

// authCheck probes the session on a schedule; the single ensureSession gateway
// decides whether a re-login (UI restart) is needed.
func authCheck(state *zhihuState) {
	ensureSession(time.Now(), false)
}

// handleSessionLost pauses the feeds on a lost session and routes recovery to
// the single login gateway.
func handleSessionLost(state *zhihuState) {
	log.Printf("zhihu: session lost, feeds paused pending re-login")
	ensureSession(time.Now(), false)
}

// handleRoundErr routes a feed-round failure: session errors pause the feeds and
// go through the login gateway; everything else is surfaced as a status error.
func handleRoundErr(state *zhihuState, err error) {
	if isSessionErr(err) {
		handleSessionLost(state)
		return
	}
	pushError(err)
}

// Injectable seams so the login-gateway unit tests do not hit the network.
var (
	probeSession   = verifySession
	startLoginUIFn = startLoginUI
)

// ensureSession is the only entry point that may start the login UI. A ready
// session is validated; a missing or expired one schedules a re-login under the
// cooldown / attempt budget. Transient verify failures (network / 5xx) are kept
// as-is: they never touch the budget or pop the login window.
func ensureSession(now time.Time, force bool) {
	if AuthStatus() == AuthStatusReady {
		if err := probeSession(); err == nil {
			return
		} else if !errors.Is(err, ErrNotLoggedIn) {
			log.Printf("zhihu: session check transient error, session kept: %v", err)
			pushError(err)
			return
		}
		log.Printf("zhihu: session check failed, scheduling re-login")
	}
	scheduleLogin(now, force)
}

func scheduleLogin(now time.Time, force bool) {
	authMu.Lock()
	if !force && now.Before(reLogin.nextAt) {
		authMu.Unlock()
		log.Printf("zhihu: re-login in cooldown until %s", reLogin.nextAt.Format("15:04:05"))
		return
	}
	if !force && reLogin.attempts >= reLoginMaxAttempts {
		authMu.Unlock()
		log.Printf("zhihu: re-login attempts exhausted (%d); open zhihu login manually or restart", reLogin.attempts)
		return
	}
	reLogin.attempts++
	reLogin.nextAt = now.Add(reLoginCooldown)
	authMu.Unlock()

	url, err := startLoginUIFn()
	if err != nil {
		log.Printf("zhihu: start login UI: %v", err)
		return
	}
	log.Printf("zhihu: login UI available at %s (auto-opened, or open it manually)", url)
}

// reLogin tracks the auto-relogin guard budget.
var reLogin = struct {
	attempts int
	nextAt   time.Time
}{}

func isSessionErr(err error) bool {
	return err == ErrNotLoggedIn
}

func stringIt(v int64) string {
	return itoa(v)
}

const digits = "0123456789"

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	buf := make([]byte, 0, 20)
	for v > 0 {
		buf = append(buf, digits[v%10])
		v /= 10
	}
	if neg {
		buf = append(buf, '-')
	}
	for l, r := 0, len(buf)-1; l < r; l, r = l+1, r-1 {
		buf[l], buf[r] = buf[r], buf[l]
	}
	return string(buf)
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
	log.Printf("zhihu: risk control, throttling %s", rateBackoff)
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
	return filepath.Join(home, ".config", "fedlet", "zhihu-state.json")
}

func loadState(path string) *zhihuState {
	data, err := os.ReadFile(path)
	if err != nil {
		return newState()
	}
	var s zhihuState
	if err := json.Unmarshal(data, &s); err != nil {
		log.Printf("zhihu: load state parse error: %v", err)
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

func saveState(path string, s *zhihuState) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		log.Printf("zhihu: save state marshal error: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		log.Printf("zhihu: save state mkdir error: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("zhihu: save state write error: %v", err)
	}
}

func pruneState(s *zhihuState, now time.Time) {
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
