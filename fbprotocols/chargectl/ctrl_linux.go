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

// reprobeAfter is the number of consecutive failed writes after which the loop
// re-probes the hardware, in case the chosen node was removed (hot-unplug, a
// driver reload). Failures are cheap to detect and re-probing resets everything.
const reprobeAfter = 3

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
	lastState  int
	lastPlug   bool
	havePlug   bool
	warned     bool
	writeFails int
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
			if st.writeFails >= reprobeAfter {
				if nc := probeController(); nc != nil {
					logf("re-probing after %d failed writes (now %s, mode=%s)", st.writeFails, nc.label, nc.modeName())
					ctl = nc
				}
				st.writeFails = 0
			}
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

	if capacity < 0 {
		pushError(errors.New("battery capacity unreadable"))
		return
	}

	switch ctl.mode {
	case modeThreshold:
		// Kernel owns the hysteresis: charge below start, stop at end.
		if err := ctl.ensureThreshold(); err != nil {
			st.writeFails++
			pushError(fmt.Errorf("threshold write: %w", err))
		} else {
			st.writeFails = 0
		}
		ctl.checkEffective(name, capacity, plugged, st)
		return
	case modeLevel:
		// End-threshold only: charge up to PauseAt and stop; recharges below it,
		// so no resume step is needed.
		if err := ctl.ensureLevel(); err != nil {
			st.writeFails++
			pushError(fmt.Errorf("level write: %w", err))
		} else {
			st.writeFails = 0
		}
		ctl.checkEffective(name, capacity, plugged, st)
		return
	}

	if !plugged {
		return
	}

	// Stateless hysteresis: derive the wanted state from the capacity on every
	// tick and let the lazy write make it idempotent. This survives restarts and
	// any stale hardware state left behind by a previous run.
	desired := -1 // -1: leave as-is
	switch {
	case capacity >= PauseAt:
		desired = 1
	case capacity <= ResumeAt:
		desired = 0
	}
	if desired < 0 {
		st.lastState = -1
		return
	}
	var err error
	if desired == 1 {
		err = ctl.stopCharging()
	} else {
		err = ctl.startCharging()
	}
	if err != nil {
		st.writeFails++
		pushError(fmt.Errorf("apply: %w", err))
		return
	}
	st.writeFails = 0
	if desired != st.lastState {
		if desired == 1 {
			logf("capacity=%d%% >= %d%%: charging stopped", capacity, PauseAt)
		} else {
			logf("capacity=%d%% <= %d%%: charging resumed", capacity, ResumeAt)
		}
		st.lastState = desired
	}
	ctl.checkEffective(name, capacity, plugged, st)
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

// externalSupplies are the supply "type" values that mean an attached charger.
var externalSupplies = map[string]bool{
	"Mains": true, "USB": true, "USB_C": true, "USB_DCP": true,
	"USB_CDP": true, "USB_ACA": true, "USB_PD": true, "USB_HVDCP": true,
	"Wireless": true,
}

// plugNames are supply device names that mean an attached charger regardless of
// the reported type (ACC also watches dc/online and pc_port/online).
var plugNames = map[string]bool{"dc": true, "pc_port": true}

// readPlugged reports whether any external supply is online.
func readPlugged() bool {
	base := resolve(powerSupplyDir)
	entries, err := os.ReadDir(base)
	if err != nil {
		return false
	}
	for _, e := range entries {
		dir := filepath.Join(base, e.Name())
		typ, _ := readSysfsString(dir, "type")
		if !externalSupplies[typ] && !plugNames[e.Name()] {
			continue
		}
		if v, ok := readSysfsInt(dir, "online"); ok && v > 0 {
			return true
		}
	}
	return false
}

// idleCurrentUA is the current below which a "Charging" status is treated as
// idle, mirroring ACC's batt_status_workaround (a kernel may report Charging
// while the pack draws no current).
const idleCurrentUA = 40000

// checkEffective emits a one-shot warning when a configured limit does not look
// like it took effect: the charger is attached, capacity is at or above PauseAt,
// yet the pack still reports active charging. It never changes state.
func (c *controller) checkEffective(name string, capacity int, plugged bool, st *loopState) {
	if !plugged || capacity < PauseAt {
		st.warned = false
		return
	}
	if st.warned {
		return
	}
	if chargingNow(name) {
		logf("warning: %s is at %d%% (>=%d%%) but still charging via %s; the node may be ineffective", name, capacity, PauseAt, c.label)
		st.warned = true
	}
}

// chargingNow reports whether the pack is drawing meaningful charge current.
func chargingNow(name string) bool {
	dir := filepath.Join(resolve(powerSupplyDir), name)
	if status, _ := readSysfsString(dir, "status"); status != "Charging" {
		return false
	}
	cur, ok := readSysfsInt(dir, "current_now")
	if !ok {
		// Without a current reading an idle pack and a charging one are
		// indistinguishable; stay quiet rather than risk a false alarm.
		return false
	}
	return abs64(cur) > idleCurrentUA
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// ---- controller ----

type mode int

const (
	// modeThreshold is the kernel charge-control ABI: a start and an end
	// threshold node the kernel itself enforces.
	modeThreshold mode = iota
	// modeLevel is an end-threshold-only node (ASUS, Apple, some Dell). Writing
	// PauseAt makes the pack charge up to PauseAt and stop; it recharges whenever
	// it drops below, so no resume step is needed.
	modeLevel
	// modeSwitch is a single vendor node written with an allow value or a stop
	// value; the stop value may be atCapacity.
	modeSwitch
)

func (m mode) String() string {
	switch m {
	case modeThreshold:
		return "threshold"
	case modeLevel:
		return "level"
	default:
		return "switch"
	}
}

// controller is a resolved, writable charge-control node.
type controller struct {
	mode             mode
	label            string
	startPath        string
	endPath          string
	switchPath       string
	enableVal        string
	disableVal       string
	chargeTypePath   string
	chargeTypeDone   bool
	chargeTypeWarned bool
	degraded         bool
}

func (c *controller) modeName() string { return c.mode.String() }

// ensureThreshold writes the fixed hysteresis window. It sets charge_type first
// (some drivers gate the ABI on it), then the end threshold, then the start
// threshold. If the start write fails the node still limits charging via end, so
// it degrades to end-only mode instead of failing on every tick.
func (c *controller) ensureThreshold() error {
	c.ensureChargeType()
	if err := writeValue(c.endPath, strconv.Itoa(PauseAt)); err != nil {
		return err
	}
	if c.degraded || c.startPath == "" {
		return nil
	}
	if err := writeValue(c.startPath, strconv.Itoa(ResumeAt)); err != nil {
		logf("start threshold write failed (%v); falling back to end=%d only", err, PauseAt)
		c.degraded = true
		c.mode = modeLevel
	}
	return nil
}

// ensureLevel keeps an end-threshold-only node at PauseAt.
func (c *controller) ensureLevel() error {
	c.ensureChargeType()
	return writeValue(c.endPath, strconv.Itoa(PauseAt))
}

// ensureChargeType writes "Custom" once so drivers that only honour the
// charge-control thresholds in Custom mode (e.g. dell_laptop) take effect. It is
// best effort: many kernels reject the value.
func (c *controller) ensureChargeType() {
	if c.chargeTypePath == "" || c.chargeTypeDone {
		return
	}
	if err := writeValue(c.chargeTypePath, "Custom"); err != nil {
		if !c.chargeTypeWarned {
			logf("charge_type write failed (%v); will retry, thresholds may be ignored", err)
			c.chargeTypeWarned = true
		}
		return
	}
	c.chargeTypeDone = true
}

// stopCharging writes the stop value.
func (c *controller) stopCharging() error {
	return writeValue(c.switchPath, c.disableVal)
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
		return &controller{mode: modeThreshold, label: c.label, startPath: start, endPath: end, chargeTypePath: findChargeType(end)}
	case modeLevel:
		end := firstMatch(c.endPat)
		if end == "" {
			return nil
		}
		if !probeAccess(end) {
			return nil
		}
		return &controller{mode: modeLevel, label: c.label, endPath: end, chargeTypePath: findChargeType(end)}
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

// findChargeType returns a writable charge_type (or charge_types) beside end,
// or "".
func findChargeType(end string) string {
	dir := filepath.Dir(end)
	for _, name := range []string{"charge_type", "charge_types"} {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && probeAccess(p) {
			return p
		}
	}
	return ""
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
		if err = os.WriteFile(path, []byte(val+"\n"), 0o644); err == nil {
			return nil
		}
		// Some nodes reject the trailing newline; retry the bare value.
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
