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
	lastState   int
	lastPlug    bool
	havePlug    bool
	lastCap     int
	haveCap     bool
	warned      bool
	writeFails  int
	ineffective int
}

func runLoop(stop <-chan struct{}) {
	logf("start: pause>=%d%% resume<=%d%% interval=%s", pauseAt(), resumeAt(), Interval)
	defer logf("stopped")

	bad := map[string]bool{}
	ctl := probeControllerSkip(bad)
	if ctl == nil {
		// No writable node anywhere. Do NOT give up: a node may appear later
		// (module load, SELinux relabel, driver rebind). Keep reporting status.
		setAuthStatus("no-backend")
		pushError(errors.New("no controllable charge node found yet; retrying"))
		logf("no controllable charge node found; will retry each tick")
	} else {
		logf("using %s (mode=%s)", ctl.label, ctl.modeName())
	}

	st := &loopState{}
	if ctl != nil {
		checkOnce(ctl, st)
	}

	ticker := time.NewTicker(Interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			switch {
			case ctl == nil:
				if nc := probeControllerSkip(bad); nc != nil {
					ctl = nc
					setAuthStatus(authStatusLocal)
					logf("found %s (mode=%s)", ctl.label, ctl.modeName())
				}
			case st.writeFails >= reprobeAfter || st.ineffective >= effectiveStrikes:
				bad[ctl.label] = true
				if nc := probeControllerSkip(bad); nc != nil {
					logf("switching backend: %s -> %s (writeFails=%d ineffective=%d)",
						ctl.label, nc.label, st.writeFails, st.ineffective)
					ctl = nc
				} else {
					logf("no alternative backend after %s; keeping it", ctl.label)
				}
				st.writeFails, st.ineffective = 0, 0
			}
			if ctl != nil {
				checkOnce(ctl, st)
			}
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
	plugged := readPlugged(name)

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
		// End-threshold only: charge up to pauseAt() and stop; recharges below it,
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

	// Stateless hysteresis: derive the wanted state from the capacity on every
	// tick and let the lazy write make it idempotent. This survives restarts and
	// any stale hardware state left behind by a previous run.
	desired := -1 // -1: leave as-is
	switch {
	case capacity >= pauseAt():
		desired = 1
	case capacity <= resumeAt():
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
			logf("capacity=%d%% >= %d%%: charging stopped", capacity, pauseAt())
		} else {
			logf("capacity=%d%% <= %d%%: charging resumed", capacity, resumeAt())
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
	"USB_CDP": true, "USB_ACA": true, "USB_PD": true, "USB_PD_DRP": true,
	"USB_PD_VBUS": true, "USB_HVDCP": true, "USB_HVDCP_2": true,
	"USB_HVDCP_3": true, "USB_HVDCP_3P5": true, "USB_PPS": true,
	"USB_FLOAT": true, "Wireless": true, "WPC": true,
}

// plugNames are supply device names that mean an attached charger regardless of
// the reported type (ACC also watches dc/online and pc_port/online).
var plugNames = map[string]bool{
	"dc": true, "pc_port": true, "ac": true, "mains": true, "main": true,
	"usb": true, "usb_c": true, "wireless": true, "wpc": true, "ac_adapter": true,
}

// readPlugged reports whether the machine is on external power. Vendor kernels
// report presence inconsistently, so it ORs every signal available: an external
// supply online, an external supply present, the battery reporting Charging or
// Full, or a positive charge current. A false "plugged" only costs one extra
// (idempotent) stop write and log line; a false "unplugged" is what let the pack
// charge to 100%, so we bias towards true.
func readPlugged(battName string) bool {
	base := resolve(powerSupplyDir)
	entries, err := os.ReadDir(base)
	if err == nil {
		for _, e := range entries {
			dir := filepath.Join(base, e.Name())
			if typ, ok := readSysfsString(dir, "type"); ok && typ == "Battery" {
				continue
			}
			typ, _ := readSysfsString(dir, "type")
			name := strings.ToLower(e.Name())
			if !externalSupplies[typ] && !plugNames[name] &&
				!strings.HasPrefix(name, "usb") && !strings.HasPrefix(name, "ac") &&
				!strings.HasPrefix(name, "mtk-") && !strings.HasPrefix(name, "smb") {
				continue
			}
			if v, ok := readSysfsInt(dir, "online"); ok && v > 0 {
				return true
			}
			if v, ok := readSysfsInt(dir, "present"); ok && v > 0 {
				return true
			}
		}
	}
	return batteryPlugged(battName)
}

// batteryPlugged infers external power from the battery's own attributes when
// no supply device reports it (common on Android with vendor chargers).
func batteryPlugged(name string) bool {
	dir := filepath.Join(resolve(powerSupplyDir), name)
	if s, ok := readSysfsString(dir, "status"); ok && (s == "Charging" || s == "Full") {
		return true
	}
	if v, ok := readSysfsInt(dir, "current_now"); ok && v > idleCurrentUA {
		return true
	}
	return false
}

// idleCurrentUA is the current below which a "Charging" status is treated as
// idle, mirroring ACC's batt_status_workaround (a kernel may report Charging
// while the pack draws no current).
const idleCurrentUA = 40000

// checkEffective warns once and counts ticks when a configured limit does not
// look like it took effect: on wall power, capacity is at or above pauseAt and
// the pack is still climbing or reports active charging. The capacity trend is
// the primary signal (it catches a node that writes OK but does nothing, even
// when the kernel lies about status/current); chargingNow is a secondary hint.
// The counter drives the loop's backend fallback.
func (c *controller) checkEffective(name string, capacity int, plugged bool, st *loopState) {
	rising := st.haveCap && capacity > st.lastCap
	st.lastCap, st.haveCap = capacity, true
	if capacity < pauseAt() || !plugged || (!rising && !chargingNow(name)) {
		st.warned = false
		st.ineffective = 0
		return
	}
	st.ineffective++
	if !st.warned {
		logf("warning: %s is at %d%% (>=%d%%) but still charging via %s; the node may be ineffective", name, capacity, pauseAt(), c.label)
		st.warned = true
	}
}

// chargingNow reports whether the pack is drawing meaningful charge current.
func chargingNow(name string) bool {
	dir := filepath.Join(resolve(powerSupplyDir), name)
	status, _ := readSysfsString(dir, "status")
	if status != "Charging" && status != "Full" {
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
	// pauseAt() makes the pack charge up to that level and stop; it recharges
	// whenever it drops below, so no resume step is needed.
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
	if err := writeValue(c.endPath, strconv.Itoa(pauseAt())); err != nil {
		return err
	}
	if c.degraded || c.startPath == "" {
		return nil
	}
	if err := writeValue(c.startPath, strconv.Itoa(resumeAt())); err != nil {
		logf("start threshold write failed (%v); falling back to end=%d only", err, pauseAt())
		c.degraded = true
		c.mode = modeLevel
	}
	return nil
}

// ensureLevel keeps an end-threshold-only node at pauseAt().
func (c *controller) ensureLevel() error {
	c.ensureChargeType()
	return writeValue(c.endPath, strconv.Itoa(pauseAt()))
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
func probeController() *controller { return probeControllerSkip(nil) }

// probeControllerSkip is probeController that ignores any candidate whose label
// is in skip, so the loop can move on from a node that resolved but does nothing.
func probeControllerSkip(skip map[string]bool) *controller {
	for _, cand := range orderedCandidates() {
		if skip[cand.label] {
			continue
		}
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
