// Package chargectl keeps this machine's battery from sitting at 100%.
//
// It is a purely local backend: it never publishes and never receives. While
// fedbridge runs it re-asserts a charging limit on the battery hardware so the
// pack spends its life around the middle of its range instead of pinned at full.
// The policy is fixed (pause at PauseAt %, resume at ResumeAt %) and there is no
// configuration file and no flag — the whole point is not to be another knob.
package chargectl

import (
	"log"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// PauseAt is the capacity (%) at or above which charging is stopped.
	PauseAt = 75
	// ResumeAt is the capacity (%) at or below which charging is allowed again.
	// The gap to PauseAt is the hysteresis that stops a full pack from toggling
	// the charger on and off every tick.
	ResumeAt = 30
	// Interval is how often the battery state is re-checked.
	Interval = 30 * time.Second
)

// authStatusLocal is reported for a backend that needs no credentials.
const authStatusLocal = "local"

var (
	statusRunning        atomic.Bool
	statusConnectedSince atomic.Value
	statusLastErrsMu     sync.Mutex
	statusLastErrs       [3]error

	// loopMu guards the loop handles shared with ctrl_linux.go / ctrl_stub.go.
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
func AuthStatus() string { return authStatusLocal }
