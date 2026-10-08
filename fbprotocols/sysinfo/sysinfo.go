package sysinfo

// Protocol orchestrator: Start / poll loop / status state / publishing.
//
// A single sampler covers the whole machine: host, cpu, mem, load, disk, io,
// net, temperature and battery. Every round produces one Snapshot which is
// marshalled and forwarded as a single entry with the standard top-level
// proto_type=sysinfo and cycle_count=1 fields (fbshared.InsertFlatFields).
//
// Timing: the loop sleeps for a full interval before the first round, so the
// very first sample lands after the HTTP listener in fedbridge/main.go is up
// (StartFn is invoked before ListenAndServe) and the periods stay aligned.
//
// State (~/.config/fedlet/sysinfo-state.json) carries no metrics — only
// last_ok_at and the most recent round's errors, so a restart does not
// resurrect stale readings.
//
// Derived rates (cpu%, io/net speeds) need a previous sample, hence they are
// -1 on the first round (rule C).

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

// stateFileBase names both the state file and the log prefix.
const stateFileBase = "sysinfo"

var (
	pubfn_   func(any) error
	mu       sync.Mutex
	runIntv  time.Duration
	loopMu   sync.Mutex
	loopStop chan struct{}
	loopDone chan struct{}
)

// SetPublishInfo wires the publish function supplied by fedbridge.
func SetPublishInfo(pubfn func(any) error) {
	pubfn_ = pubfn
}

func publish(v any) error {
	if pubfn_ == nil {
		return nil
	}
	return pubfn_(v)
}

// Start launches the polling loop. A non-positive interval falls back to
// DefaultInterval (123s). Calling Start again stops any previous loop first
// and waits for it to finish, so no two loops ever publish in parallel.
func Start(interval time.Duration) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	mu.Lock()
	runIntv = interval
	mu.Unlock()

	loopMu.Lock()
	stopLocked()
	ch := make(chan struct{})
	done := make(chan struct{})
	loopStop, loopDone = ch, done
	loopMu.Unlock()

	go func() {
		pollLoop(ch)
		close(done)
	}()
}

// Stop halts the polling loop and blocks until it has fully exited, so callers
// may safely tear down shared state afterwards. Safe when nothing is running.
func Stop() {
	loopMu.Lock()
	defer loopMu.Unlock()
	stopLocked()
}

// stopLocked closes the stop channel and waits for the loop goroutine to
// return. Callers must hold loopMu.
func stopLocked() {
	if loopStop == nil {
		return
	}
	close(loopStop)
	<-loopDone
	loopStop, loopDone = nil, nil
}

func currentInterval() time.Duration {
	mu.Lock()
	defer mu.Unlock()
	if runIntv <= 0 {
		return DefaultInterval
	}
	return runIntv
}

// stateFilePathFn is an indirection point so the loop can be tested without
// touching the developer's real state file.
var stateFilePathFn = stateFilePath

// pollLoop samples on a ticker. The first tick is a full interval away, which
// is deliberate: the first round must not race fedbridge's HTTP server, whose
// ListenAndServe runs after StartFn.
func pollLoop(stop <-chan struct{}) {
	statusRunning.Store(true)
	statusConnectedSince.Store(time.Now())
	defer statusRunning.Store(false)

	iv := currentInterval()
	state := loadState(stateFilePathFn())
	logPrefix("started interval=%s", iv)

	ticker := time.NewTicker(iv)
	defer ticker.Stop()

	var prev *speedSnapshot
	for {
		select {
		case <-ticker.C:
			prev = round(state, prev)
		case <-stop:
			logPrefix("stopped")
			return
		}
	}
}

// round collects, publishes and persists one snapshot, returning the counter
// snapshot the next round diffs against.
func round(state *sysinfoState, prev *speedSnapshot) *speedSnapshot {
	snap, next := collectOnce(prev)

	if err := publishSnapshot(snap); err != nil {
		logPrefix("publish error: %v", err)
		pushError(err)
	}

	state.LastOkAt = time.Now().Unix()
	state.LastErrs = trimErrs(snap.Errors, 3)
	saveState(stateFilePathFn(), state)

	logPrefix("round cpu=%s%% mem=%s%% disks=%d io=%d net=%d temp=%d batt=%d errs=%d",
		fmtPercent(snap.CPU.Percent), fmtPercent(snap.Mem.UsedPercent),
		len(snap.Disk), len(snap.IO), len(snap.Net), len(snap.Temp),
		len(snap.Battery), len(snap.Errors))
	return next
}

// publishSnapshot marshals the snapshot and forwards it with the standard
// top-level fields. cycle_count is 1 because one round emits exactly one entry.
func publishSnapshot(s *Snapshot) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	flat, err := fbshared.InsertFlatFields(raw, map[string]any{
		"proto_type":  "sysinfo",
		"cycle_count": 1,
	})
	if err != nil {
		return err
	}
	return publish(flat)
}

// ---- persisted status state ----

// sysinfoState is the on-disk status record: no metrics, only liveness and the
// tail of the last round's errors.
type sysinfoState struct {
	LastOkAt int64    `json:"last_ok_at"`
	LastErrs []string `json:"last_errs,omitempty"`
}

func newState() *sysinfoState { return &sysinfoState{} }

func stateFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "fedlet", stateFileBase+"-state.json")
}

func loadState(path string) *sysinfoState {
	data, err := os.ReadFile(path)
	if err != nil {
		return newState()
	}
	var s sysinfoState
	if err := json.Unmarshal(data, &s); err != nil {
		logPrefix("load state parse error: %v", err)
		return newState()
	}
	return &s
}

func saveState(path string, s *sysinfoState) {
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

// trimErrs keeps at most n leading entries for the persisted record.
func trimErrs(in []string, n int) []string {
	if len(in) == 0 || n <= 0 {
		return nil
	}
	if len(in) > n {
		in = in[:n]
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
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

// AuthStatus reports the collection mode: nothing here needs credentials.
func AuthStatus() string { return authStatusLocal }

// AuthUser identifies the backend.
func AuthUser() string { return stateFileBase }

// authStatusLocal is the value published for a purely local collector.
const authStatusLocal = "local"

// ---- shared small helpers ----

func logPrefix(format string, args ...any) {
	log.Printf(stateFileBase+": "+format, args...)
}

// fmtPercent renders a percentage for the log line, using "?" for the -1
// sentinel so a first round reads clearly rather than as "cpu=-1.0%".
func fmtPercent(v float64) string {
	if v == InvalidNum {
		return "?"
	}
	return trimFloat(v)
}

func trimFloat(v float64) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	return string(b)
}
