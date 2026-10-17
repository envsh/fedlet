//go:build linux || android

package chargectl

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// sysRoot is the mount point of the sysfs tree. It is a var so tests can point
// the whole package at a fixture directory; every absolute /sys path is funnelled
// through resolve.
var sysRoot = "/sys"

// powerSupplyDir is where the kernel publishes supply devices.
var powerSupplyDir = "/sys/class/power_supply"

// writeRetryDelay is slept between failed sysfs writes. A var so tests can
// eliminate the wait.
var writeRetryDelay = time.Second

// writeRetries bounds the attempts made for a single attribute write. Some
// drivers transiently return EBUSY right after a charger event.
const writeRetries = 6

// atCapacity is the pseudo-value meaning "the battery's current capacity".
// ACC writes it to charge_stop_level to stop charging where the pack is now.
const atCapacity = "@capacity"

// Start launches the control loop. It is idempotent: a second call while the
// loop is running does nothing.
func Start() {
	loopMu.Lock()
	defer loopMu.Unlock()
	if loopStop != nil {
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	loopStop, loopDone = stop, done
	setRunning(true)
	go func() {
		runLoop(stop)
		close(done)
	}()
}

// Stop halts the control loop and waits for it to exit. Safe when not running.
func Stop() {
	loopMu.Lock()
	defer loopMu.Unlock()
	if loopStop == nil {
		return
	}
	close(loopStop)
	<-loopDone
	loopStop, loopDone = nil, nil
	setRunning(false)
}

// loopState is per-run state. It is deliberately not persisted: a restart
// re-probes the hardware and re-derives everything from the current charge.
type loopState struct {
	paused   bool
	lastPlug bool
	havePlug bool
}

func runLoop(stop <-chan struct{}) {
	logf("start: pause>=%d%% resume<=%d%% interval=%s", PauseAt, ResumeAt, Interval)
	defer logf("stopped")

	ctl := probeController()
	if ctl == nil {
		err := errors.New("no controllable charge node found")
		pushError(err)
		logf("%v; giving up", err)
		return
	}
	logf("using %s (mode=%s)", ctl.label, ctl.modeName())

	st := &loopState{}
	checkOnce(ctl, st)

	ticker := time.NewTicker(Interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			checkOnce(ctl, st)
		}
	}
}

// checkOnce is one control decision: read the battery, notice plug events, and
// apply the hysteresis. All errors are recorded rather than fatal.
func checkOnce(ctl *controller, st *loopState) {
	name, capacity, ok := readBattery()
	if !ok {
		pushError(errors.New("no battery present"))
		return
	}
	plugged := readPlugged()

	if !st.havePlug || plugged != st.lastPlug {
		if plugged {
			logf("charger plugged in (battery=%s)", name)
		} else {
			logf("charger unplugged (battery=%s)", name)
		}
		st.lastPlug, st.havePlug = plugged, true
	}

	// Threshold mode lets the kernel own the hysteresis: once both thresholds
	// are set it charges below the start level and stops at the end level with
	// no further help from us.
	if ctl.mode == modeThreshold {
		if err := ctl.ensureThreshold(); err != nil {
			pushError(fmt.Errorf("threshold write: %w", err))
		}
		return
	}

	if capacity < 0 {
		pushError(errors.New("battery capacity unreadable"))
		return
	}

	// Nothing can be done while unplugged; when the charger returns the state
	// below re-applies the limit if the pack is still above the pause level.
	if !plugged {
		return
	}

	switch {
	case capacity >= PauseAt && !st.paused:
		if err := ctl.stopCharging(capacity); err != nil {
			pushError(fmt.Errorf("pause: %w", err))
			return
		}
		st.paused = true
		logf("capacity=%d%% >= %d%%: charging paused", capacity, PauseAt)
	case capacity <= ResumeAt && st.paused:
		if err := ctl.startCharging(); err != nil {
			pushError(fmt.Errorf("resume: %w", err))
			return
		}
		st.paused = false
		logf("capacity=%d%% <= %d%%: charging resumed", capacity, ResumeAt)
	}
}

// readBattery returns the first present Battery supply's name and capacity (%).
// ok=false means there is no battery to manage.
func readBattery() (string, int, bool) {
	base := resolve(powerSupplyDir)
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", -1, false
	}
	for _, e := range entries {
		dir := filepath.Join(base, e.Name())
		if typ, ok := readSysfsString(dir, "type"); !ok || typ != "Battery" {
			continue
		}
		if v, ok := readSysfsInt(dir, "present"); ok && v == 0 {
			continue
		}
		capacity := -1
		if v, ok := readSysfsInt(dir, "capacity"); ok {
			capacity = int(v)
		}
		return e.Name(), capacity, true
	}
	return "", -1, false
}

// readPlugged reports whether any external supply is online. The supply type
// names cover Mains/USB/Wireless and the USB variants Android drivers use.
func readPlugged() bool {
	base := resolve(powerSupplyDir)
	entries, err := os.ReadDir(base)
	if err != nil {
		return false
	}
	for _, e := range entries {
		dir := filepath.Join(base, e.Name())
		typ, ok := readSysfsString(dir, "type")
		if !ok {
			continue
		}
		switch typ {
		case "Mains", "USB", "USB_C", "USB_DCP", "USB_CDP", "USB_ACA", "USB_PD", "USB_HVDCP", "Wireless":
			if v, ok := readSysfsInt(dir, "online"); ok && v > 0 {
				return true
			}
		}
	}
	return false
}

// ---- controller ----

type mode int

const (
	// modeThreshold is the kernel charge-control ABI: a start and an end
	// threshold node the kernel itself enforces.
	modeThreshold mode = iota
	// modeSwitch is a single vendor node written with an allow value or a stop
	// value; the stop value may be atCapacity.
	modeSwitch
)

func (m mode) String() string {
	if m == modeThreshold {
		return "threshold"
	}
	return "switch"
}

// controller is a resolved, writable charge-control node.
type controller struct {
	mode       mode
	label      string
	startPath  string
	endPath    string
	switchPath string
	enableVal  string
	disableVal string
}

func (c *controller) modeName() string { return c.mode.String() }

// ensureThreshold writes the fixed hysteresis window. writeValue is lazy, so on
// an already-correct node this is a read-only no-op.
func (c *controller) ensureThreshold() error {
	if err := writeValue(c.startPath, strconv.Itoa(ResumeAt)); err != nil {
		return err
	}
	return writeValue(c.endPath, strconv.Itoa(PauseAt))
}

// stopCharging writes the stop value, expanding atCapacity to the measured
// capacity so charge_stop_level halts where the pack is now.
func (c *controller) stopCharging(capacity int) error {
	val := c.disableVal
	if val == atCapacity {
		val = strconv.Itoa(capacity)
	}
	return writeValue(c.switchPath, val)
}

func (c *controller) startCharging() error {
	return writeValue(c.switchPath, c.enableVal)
}

// candidate is one entry of a hardware table: either a threshold pair or a
// single switch node with its allow/stop values.
type candidate struct {
	label      string
	mode       mode
	startPat   string
	endPat     string
	pat        string
	enableVal  string
	disableVal string
}

// resolve expands the candidate's glob(s) and returns a controller only if the
// node exists and is writable.
func (c candidate) resolve() *controller {
	switch c.mode {
	case modeThreshold:
		end := firstMatch(c.endPat)
		if end == "" {
			return nil
		}
		start := filepath.Join(filepath.Dir(end), "charge_control_start_threshold")
		if fi, err := os.Stat(start); err != nil || fi.IsDir() {
			return nil
		}
		if !probeAccess(end) || !probeAccess(start) {
			return nil
		}
		return &controller{mode: modeThreshold, label: c.label, startPath: start, endPath: end}
	default:
		p := firstMatch(c.pat)
		if p == "" {
			return nil
		}
		if !probeAccess(p) {
			return nil
		}
		return &controller{mode: modeSwitch, label: c.label, switchPath: p, enableVal: c.enableVal, disableVal: c.disableVal}
	}
}

// probeController walks the ordered tables and returns the first usable node.
func probeController() *controller {
	for _, cand := range orderedCandidates() {
		if c := cand.resolve(); c != nil {
			return c
		}
	}
	return nil
}

// firstMatch expands pat (absolute, or relative to power_supply) and returns the
// first regular-file match, or "".
func firstMatch(pat string) string {
	if pat == "" {
		return ""
	}
	full := pat
	if !strings.HasPrefix(pat, "/") {
		full = filepath.Join(resolve(powerSupplyDir), pat)
	} else {
		full = resolve(pat)
	}
	matches, err := filepath.Glob(full)
	if err != nil {
		return ""
	}
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && !fi.IsDir() {
			return m
		}
	}
	return ""
}

// resolve maps an absolute /sys path onto sysRoot so tests can sandbox it.
func resolve(p string) string {
	if sysRoot == "/sys" {
		return p
	}
	if p == "/sys" {
		return sysRoot
	}
	if strings.HasPrefix(p, "/sys/") {
		return filepath.Join(sysRoot, strings.TrimPrefix(p, "/sys/"))
	}
	return p
}

// ---- sysfs helpers ----

// writeValue writes val to path, skipping the write when the node already holds
// that value. Failures are retried writeRetries times, writeRetryDelay apart.
func writeValue(path, val string) error {
	if cur, err := os.ReadFile(path); err == nil {
		if strings.TrimSpace(string(cur)) == val {
			return nil
		}
	}
	var err error
	for i := 0; i < writeRetries; i++ {
		if err = os.WriteFile(path, []byte(val), 0o644); err == nil {
			return nil
		}
		if i < writeRetries-1 {
			time.Sleep(writeRetryDelay)
		}
	}
	return err
}

// readSysfsInt parses a single integer attribute; ok=false when the file is
// absent, unreadable or not numeric.
func readSysfsInt(dir, name string) (int64, bool) {
	raw, ok := readSysfsString(dir, name)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// readSysfsString returns the trimmed contents of an attribute file.
func readSysfsString(dir, name string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}
