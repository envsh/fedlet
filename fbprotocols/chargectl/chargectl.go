// Package chargectl keeps this machine's battery from sitting at 100%.
//
// It is a purely local backend: it never publishes and never receives. While
// fedbridge runs it re-asserts a charging limit on the battery hardware so the
// pack spends its life around the middle of its range instead of pinned at full.
// The window defaults to DefaultPauseAt/DefaultResumeAt and is runtime-tunable
// via SetThresholds; fedbridge wires that to the chargectl-pause-at /
// chargectl-resume-at settings (env FEDLET_CHARGECTL_PAUSE_AT / _RESUME_AT,
// ~/.config/fedlet/config.json and the web config page). There is deliberately
// no command-line flag.
package chargectl

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultPauseAt is the capacity (%) at or above which charging is stopped.
	DefaultPauseAt = 75
	// DefaultResumeAt is the capacity (%) at or below which charging is allowed
	// again. The gap to pauseAt() is the hysteresis that stops a full pack from
	// toggling the charger on and off every tick.
	DefaultResumeAt = 30
	// Interval is how often the battery state is re-checked.
	Interval = 30 * time.Second
)

// reprobeAfter is the number of consecutive failed writes after which the loop
// re-probes the hardware, in case the chosen node or backend went away.
const reprobeAfter = 3

// thresholds holds the active {pause, resume} window; nil means the defaults.
var thresholds atomic.Pointer[[2]int]

// ValidateWindow rejects an out-of-range pause/resume pair, naming the offending
// value and its allowed range so both the web page and the log carry a clear hint.
func ValidateWindow(pause, resume int) error {
	if pause < 1 || pause > 100 {
		return fmt.Errorf("停充阈值需为 1..100 的整数(当前 %d)", pause)
	}
	if resume < 0 || resume > 100 {
		return fmt.Errorf("恢复阈值需在 0..100(当前 %d)", resume)
	}
	if resume >= pause {
		return fmt.Errorf("恢复阈值需小于停充阈值(恢复 %d,停充 %d)", resume, pause)
	}
	return nil
}

// SetThresholds replaces the runtime window after ValidateWindow passes.
func SetThresholds(pause, resume int) error {
	if err := ValidateWindow(pause, resume); err != nil {
		return err
	}
	v := [2]int{pause, resume}
	thresholds.Store(&v)
	return nil
}

// Thresholds returns the active pause/resume window (defaults when unset).
func Thresholds() (pause, resume int) {
	if v := thresholds.Load(); v != nil {
		return v[0], v[1]
	}
	return DefaultPauseAt, DefaultResumeAt
}

func pauseAt() int  { p, _ := Thresholds(); return p }
func resumeAt() int { _, r := Thresholds(); return r }

// authStatusLocal is reported for a backend that needs no credentials.
const authStatusLocal = "local"

// authStatusOverride lets a platform backend surface a degraded mode (the macOS
// read-only fallback) through the same AuthStatus field.
var authStatusOverride atomic.Value

func setAuthStatus(s string) { authStatusOverride.Store(s) }

var (
	statusRunning        atomic.Bool
	statusConnectedSince atomic.Value
	statusLastErrsMu     sync.Mutex
	statusLastErrs       [3]error

	// loopMu guards the loop handles shared with ctrl_linux.go / ctrl_darwin.go / ctrl_stub.go.
	loopMu   sync.Mutex
	loopStop chan struct{}
	loopDone chan struct{}
)

// logf prefixes every line so chargectl is easy to pick out of the bridge log.
func logf(format string, args ...any) {
	log.Printf("chargectl: "+format, args...)
}

func setRunning(v bool) {
	statusRunning.Store(v)
	if v {
		statusConnectedSince.Store(time.Now())
	} else {
		statusConnectedSince.Store(time.Time{})
	}
}

// pushError keeps the three most recent errors, newest first, mirroring sysinfo.
func pushError(err error) {
	statusLastErrsMu.Lock()
	statusLastErrs[2] = statusLastErrs[1]
	statusLastErrs[1] = statusLastErrs[0]
	statusLastErrs[0] = err
	statusLastErrsMu.Unlock()
}

// IsRunning reports whether the control loop is active.
func IsRunning() bool { return statusRunning.Load() }

// ConnectedSince is when the current control loop was started.
func ConnectedSince() time.Time {
	v := statusConnectedSince.Load()
	if v == nil {
		return time.Time{}
	}
	return v.(time.Time)
}

// LastErrs returns up to the last three recorded errors, newest first.
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

// AuthStatus reports the operating mode; nothing here needs credentials.
func AuthStatus() string {
	if v := authStatusOverride.Load(); v != nil {
		return v.(string)
	}
	return authStatusLocal
}
