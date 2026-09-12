package bilibili

// Orchestrator: Start/Stop, the session gateway (ensureSession), the poll
// loops, publish plumbing and the per-channel dedupe state. Mirrors the
// zhihu/xhs bridge structure so fedbridge wires it up identically.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ---- settings + runtime flags ----

type settings struct {
	hotInterval       time.Duration
	notifyInterval    time.Duration
	feedInterval      time.Duration
	authCheckInterval time.Duration
}

var (
	runMu     sync.Mutex
	running   bool
	started   bool
	stopCh    chan struct{}
	runWg     sync.WaitGroup
	connected time.Time
	set       settings
)

// dedupe lifecycle: keys older than dedupeExpiry are pruned from the persisted
// state; the disk state survives restarts.
const dedupeExpiry = 72 * time.Hour

// biliState is the per-channel dedupe bookkeeping persisted to
// ~/.config/fedlet/bilibili-state.json.
type biliState struct {
	Hotboard      map[string]int64 `json:"hotboard"`
	FollowFeed    map[string]int64 `json:"follow_feed"`
	Notifications map[string]int64 `json:"notifications"`
}

func newBiliState() *biliState {
	return &biliState{
		Hotboard:      make(map[string]int64),
		FollowFeed:    make(map[string]int64),
		Notifications: make(map[string]int64),
	}
}

func stateFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "fedlet", "bilibili-state.json")
}

func loadState() *biliState {
	st := newBiliState()
	data, err := os.ReadFile(stateFilePath())
	if err != nil {
		return st
	}
	if err := json.Unmarshal(data, st); err != nil {
		log.Printf("bilibili: state parse error: %v", err)
		return newBiliState()
	}
	if st.Hotboard == nil {
		st.Hotboard = make(map[string]int64)
	}
	if st.FollowFeed == nil {
		st.FollowFeed = make(map[string]int64)
	}
	if st.Notifications == nil {
		st.Notifications = make(map[string]int64)
	}
	return st
}

func saveState(st *biliState) {
	pruneMap(st.Hotboard)
	pruneMap(st.FollowFeed)
	pruneMap(st.Notifications)
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	p := stateFilePath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return
	}
	if err := os.WriteFile(p, data, 0600); err != nil {
		log.Printf("bilibili: save state error: %v", err)
	}
}

func pruneMap(m map[string]int64) {
	cutoff := time.Now().Add(-dedupeExpiry).Unix()
	for k, v := range m {
		if v < cutoff {
			delete(m, k)
		}
	}
}

// ---- rate gate (risk/backoff bookkeeping) ----

type rateLimiter struct {
	mu           sync.Mutex
	backoffUntil time.Time
}

var rateLimit = &rateLimiter{}

// noteRateLimit is invoked by the client whenever the server answers a
// risk/block response; the gateway then sheds rounds for a while instead of
// hammering the account.
func noteRateLimit() {
	rateLimit.mu.Lock()
	rateLimit.backoffUntil = time.Now().Add(15 * time.Second)
	rateLimit.mu.Unlock()
}

func clearRateLimit() {
	rateLimit.mu.Lock()
	rateLimit.backoffUntil = time.Time{}
	rateLimit.mu.Unlock()
}

func rateLimited() bool {
	rateLimit.mu.Lock()
	defer rateLimit.mu.Unlock()
	return time.Now().Before(rateLimit.backoffUntil)
}

// errPartialBackoff marks a round skipped because risk control demanded a pause
// (kept the session; retried next interval).
var errPartialBackoff = errors.New("bilibili: risk control backoff, round skipped")

// ---- login gateway ----

// reLogin holds the cooldown gate so a closed/timed-out login UI is not
// immediately re-opened every consecutive round (5 minutes, zhihu parity).
var reLogin = struct{ nextAt time.Time }{}

func armReLoginCooldown() {
	authMu.Lock()
	reLogin.nextAt = time.Now().Add(5 * time.Minute)
	authMu.Unlock()
}

func loginCooldownLeft() time.Duration {
	authMu.Lock()
	defer authMu.Unlock()
	d := time.Until(reLogin.nextAt)
	if d < 0 {
		return 0
	}
	return d
}

type errLoginPending struct{ url string }

func (e *errLoginPending) Error() string { return "bilibili: login UI open at " + e.url }

// loginGate opens the login UI unless a cooldown remanent is active; when a UI
// already exists it returns immediately with its URL.
func loginGate() error {
	if d := loginCooldownLeft(); d > 0 {
		return fmt.Errorf("bilibili: 登录 UI 冷却中(约 %s),稍后自动重试", d.Round(time.Second))
	}
	url, err := startLoginUIFn()
	if err != nil {
		armReLoginCooldown()
		return err
	}
	openLoginBrowserOnce(url)
	log.Printf("bilibili: login UI opened at %s", url)
	return &errLoginPending{url: url}
}

// ensureSession is the single auth gateway for every authenticated round:
//
//	T1 empty  -> load persisted credentials, then open the login UI if still empty
//	T2 ready  -> assume ok (the 30-min probe loop flips dead sessions to invalid)
//	T3 invalid-> try the refresh_token rotation, probe to confirm, else login UI
//	T5 cooldown / UI already open -> wait, never spam the account
func ensureSession() error {
	if rateLimited() {
		return errPartialBackoff
	}
	switch AuthStatus() {
	case AuthStatusReady:
		return nil
	case AuthStatusEmpty:
		loadAuth()
		if AuthStatus() == AuthStatusReady {
			return nil
		}
		return loginGate()
	case AuthStatusInvalid:
		if err := refreshCookies(); err != nil {
			if errors.Is(err, ErrNotLoggedIn) {
				return loginGate()
			}
			log.Printf("bilibili: refresh failed: %v", err)
			markSessionInvalid(err)
			return loginGate()
		}
		if err := probeSession(); err != nil {
			markSessionInvalid(err)
			return loginGate()
		}
		return nil
	default:
		return loginGate()
	}
}

// ---- rounds ----

// runAuthRound gates one authenticated round on ensureSession and never lets a
// round error crash the loop.
func runAuthRound(fn func(*biliState) int, st *biliState) {
	if err := ensureSession(); err != nil {
		pushError(err)
		return
	}
	fn(st)
}

// ---- publish plumbing ----

// publish is injected by fedbridge via SetPublishInfo. A no-op default keeps
// standalone tests working.
var publish = func(payload map[string]any) error {
	log.Printf("bilibili: publish (no injected sink) %v", payload)
	return nil
}

// SetPublishInfo wires the bridge publish sink (fedbridge calls this from its
// bilibili init). The sink takes any — the payload maps are passed through.
func SetPublishInfo(pub func(any) error) {
	if pub == nil {
		return
	}
	publish = func(payload map[string]any) error { return pub(payload) }
}

// ---- status api (consumed by fedbridge statusFn) ----

var (
	lastErrsMu sync.Mutex
	lastErrs   []error
)

func pushError(err error) {
	if err == nil {
		return
	}
	lastErrsMu.Lock()
	lastErrs = append(lastErrs, err)
	if len(lastErrs) > 10 {
		lastErrs = lastErrs[len(lastErrs)-10:]
	}
	lastErrsMu.Unlock()
	log.Printf("bilibili: %v", err)
}

// LastErrs returns a snapshot of the recent round errors.
func LastErrs() []error {
	lastErrsMu.Lock()
	defer lastErrsMu.Unlock()
	out := make([]error, len(lastErrs))
	copy(out, lastErrs)
	return out
}

// ErrLast returns the most recent round error, if any.
func ErrLast() error {
	lastErrsMu.Lock()
	defer lastErrsMu.Unlock()
	if len(lastErrs) == 0 {
		return nil
	}
	return lastErrs[len(lastErrs)-1]
}

// ConnectedSince reports when the bridge (startFn) was last launched.
func ConnectedSince() time.Time {
	runMu.Lock()
	defer runMu.Unlock()
	return connected
}

// Running reports whether the poll loops are alive.
func Running() bool {
	runMu.Lock()
	defer runMu.Unlock()
	return running
}

// ---- lifecycle ----

// Start launches the hotboard + notify loops and the follow-feed loop behind a
// single session gateway. It is safe to call before auth exists: the loops just
// wait at the login UI until a session appears. Mirrors the fedbridge StartFn
// signature of zhihu/xhs (hot/notify booleans + intervals).
func Start(hot, notify bool, hotInterval, notifyInterval time.Duration) {
	runMu.Lock()
	if started {
		runMu.Unlock()
		return
	}
	started = true
	connected = time.Now()
	set = settings{
		hotInterval:       hotInterval,
		notifyInterval:    notifyInterval,
		feedInterval:      600 * time.Second,
		authCheckInterval: 1800 * time.Second,
	}
	if set.hotInterval <= 0 {
		set.hotInterval = 600 * time.Second
	}
	if set.notifyInterval <= 0 {
		set.notifyInterval = 60 * time.Second
	}
	_ = hot
	_ = notify
	stopCh = make(chan struct{})
	running = true
	runMu.Unlock()

	ensureWarmup()
	log.Printf("bilibili: bridge starting hot=%s notify=%s feed=%s authCheck=%s",
		set.hotInterval, set.notifyInterval, set.feedInterval, set.authCheckInterval)

	runWg.Add(3)
	go pollLoopHotNotify()
	go pollLoopFeed()
	go pollLoopAuthCheck()
}

// Stop halts all loops and persists the dedupe state.
func Stop() {
	runMu.Lock()
	if !running || stopCh == nil {
		runMu.Unlock()
		return
	}
	close(stopCh)
	running = false
	runMu.Unlock()
	runWg.Wait()
}

func pollLoopHotNotify() {
	defer runWg.Done()
	st := loadState()
	hotTicker := time.NewTicker(set.hotInterval)
	notifyTicker := time.NewTicker(set.notifyInterval)
	defer hotTicker.Stop()
	defer notifyTicker.Stop()
	for {
		select {
		case <-stopCh:
			saveState(st)
			return
		case <-hotTicker.C:
			runAuthRound(func(s *biliState) int {
				return hotRound(s)
			}, st)
			saveState(st)
		case <-notifyTicker.C:
			runAuthRound(func(s *biliState) int {
				return notifyRound(s)
			}, st)
			saveState(st)
		}
	}
}

func pollLoopFeed() {
	defer runWg.Done()
	st := loadState()
	ticker := time.NewTicker(set.feedInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			saveState(st)
			return
		case <-ticker.C:
			runAuthRound(func(s *biliState) int {
				return feedRound(s)
			}, st)
			saveState(st)
		}
	}
}

func pollLoopAuthCheck() {
	defer runWg.Done()
	// Probe not instantly on boot: give the human the login window first.
	ticker := time.NewTicker(set.authCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			if AuthStatus() == AuthStatusReady {
				if err := probeSession(); err != nil && !errors.Is(err, errUnverifiable) {
					log.Printf("bilibili: periodic auth check failed: %v", err)
					pushError(err)
				}
			}
		}
	}
}
