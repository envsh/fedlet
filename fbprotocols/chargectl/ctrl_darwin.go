//go:build darwin

package chargectl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// macOS exposes no /sys power-supply tree; the SMC charge atoms and BCLM are
// only reachable through IOKit. We resolve IOKit at runtime through purego (no
// cgo, no macOS SDK, cross-compilable), and fall back to the de-facto command
// line tools `bclm`/`smc` when present. Reads use ioreg (falling back to pmset)
// and need no root. Backends are auto-selected, in order:
//
//	native SMC BCLM (Intel) -> native CH0B/CH0C -> CHTE -> CHWA -> external
//	bclm -> external smc (CH0B/CHTE/CHWA) -> read-only
//
// Caveat: recent macOS rejects some of these writes even as root (BCLM is a
// no-op on >= 15.0 without SIP disabled; CH0B/CH0C/CHTE return
// kIOReturnNotPrivileged on 26.7+/27 beta). A backend that fails or does not
// take effect is dropped and the next candidate is tried automatically.

var (
	smcBin  = envOr("FEDLET_CHARGECTL_SMC", "smc")
	bclmBin = envOr("FEDLET_CHARGECTL_BCLM", "bclm")
)

func envOr(k, dft string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return dft
}

// sudoArgs is an optional privilege prefix for the external tools. It is empty
// by default because fedbridge is expected to run as root; set
// FEDLET_CHARGECTL_SUDO (e.g. "sudo -n") only when running unprivileged.
func sudoArgs() []string {
	if v := os.Getenv("FEDLET_CHARGECTL_SUDO"); v != "" && v != "none" {
		return strings.Fields(v)
	}
	return nil
}

// runTimeout bounds every external tool call so a hung ioreg/pmset/smc/bclm
// cannot stall the control loop.
const runTimeout = 5 * time.Second

func runOutput(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

// Start launches the control loop (idempotent).
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
		runMacLoop(stop)
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

const (
	// macModeLevel is an end-threshold value (BCLM): charge up to pauseAt().
	macModeLevel = "level"
	// macModeSwitch is a boolean inhibit atom (CH0B/CHTE/CHWA).
	macModeSwitch = "switch"
	// macModeFirmware is the macOS 27-era firmware charge-limit (bfF0/bfD0/bfE0):
	// the firmware itself enforces the window, we only set it.
	macModeFirmware = "firmware"
	// adapterMinBand is the minimum hysteresis band forced in adapter mode: the
	// adapter is cut to hold the ceiling and restored to recharge, so a narrow
	// band would cycle the pack and rewrite the SMC constantly (mirrors batt).
	adapterMinBand = 5
)

// macCtl is a resolved charge-control backend: either a native SMC connection
// (h != nil) or an external tool (bin != "").
type macCtl struct {
	label string
	mode  string
	h     *smcHandle
	bin   string // external tool path
	key   string // native/tool switch atom
	stop  []byte // payload that stops charging
	allow []byte // payload that allows charging
	// adapter marks a backend that holds the ceiling by cutting wall power
	// (CHIE/CH0J/CH0I/CH0K): the pack discharges instead of charging.
	adapter bool
}

type macLoopState struct {
	lastState   int
	lastPlug    bool
	havePlug    bool
	lastCap     int
	haveCap     bool
	warned      bool
	roWarned    bool
	writeFails  int
	ineffective int
}

func runMacLoop(stop <-chan struct{}) {
	log.Printf("chargectl: start(darwin): pause>=%d%% resume<=%d%% interval=%s", pauseAt(), resumeAt(), Interval)
	defer log.Printf("chargectl: stopped")

	cands := macBackends()
	idx := 0
	var ctl *macCtl
	if len(cands) > 0 {
		ctl = cands[idx]
		setAuthStatus(authStatusLocal)
		log.Printf("chargectl: darwin: using %s (mode=%s)", ctl.label, ctl.mode)
	} else {
		setAuthStatus("read-only")
		log.Printf("chargectl: read-only: no SMC charge key and no bclm/smc tool; install one (see README)")
	}

	st := &macLoopState{}
	if ctl != nil {
		checkMacOnce(ctl, st)
	} else {
		checkMacReadOnly(st)
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
				if c := macBackends(); len(c) > 0 {
					cands, idx, ctl = c, 0, c[0]
					setAuthStatus(authStatusLocal)
					log.Printf("chargectl: darwin: found %s (mode=%s)", ctl.label, ctl.mode)
				}
			case st.writeFails >= reprobeAfter || st.ineffective >= effectiveStrikes:
				if idx+1 < len(cands) {
					log.Printf("chargectl: darwin: switching backend %s -> %s (writeFails=%d ineffective=%d)",
						ctl.label, cands[idx+1].label, st.writeFails, st.ineffective)
					idx++
					ctl = cands[idx]
				} else {
					log.Printf("chargectl: darwin: no alternative backend after %s; keeping it", ctl.label)
				}
				st.writeFails, st.ineffective = 0, 0
			}
			if ctl != nil {
				checkMacOnce(ctl, st)
			} else {
				checkMacReadOnly(st)
			}
		}
	}
}

// smcProbeKeys is the set of charge-control keys whose presence the startup log
// reports, so one run reveals the machine's writable surface.
var smcProbeKeys = []string{"bfF0", "bfD0", "bfE0", "BCLM", "BFCL", "CH0B", "CH0C", "CHTE", "CHIE", "CH0J", "CH0I", "CH0K", "CHWA"}

var smcInvOnce sync.Once

// logSMCDiagnostics reports which charge keys exist (and their data size) once
// per process, so "no suitable key" is distinguishable from "SMC would not
// open" from a single log line.
func logSMCDiagnostics(h *smcHandle) {
	smcInvOnce.Do(func() {
		var present, absent []string
		for _, k := range smcProbeKeys {
			if sz, err := h.keySize(k); err == nil {
				present = append(present, fmt.Sprintf("%s(%d)", k, sz))
			} else {
				absent = append(absent, k)
			}
		}
		log.Printf("chargectl: darwin SMC keys: present=[%s] absent=[%s]", strings.Join(present, " "), strings.Join(absent, " "))
	})
}

// macBackends probes the native SMC atoms and then the external tools, returning
// every candidate that exists. An empty result means read-only.
func macBackends() []*macCtl {
	var out []*macCtl
	if h, err := openSMC(); err == nil {
		logSMCDiagnostics(h)
		if hasAllKeys(h, "bfF0", "bfD0", "bfE0") {
			out = append(out, &macCtl{label: "SMC firmware limit bfF0 (native)", mode: macModeFirmware, h: h})
		}
		if _, err := h.keySize("BCLM"); err == nil {
			out = append(out, &macCtl{label: "SMC BCLM (native)", mode: macModeLevel, h: h, key: "BCLM"})
		}
		if _, err := h.keySize("CH0B"); err == nil {
			stop, allow := smcStopAllow("CH0B")
			out = append(out, &macCtl{label: "SMC CH0B/CH0C (native)", mode: macModeSwitch, h: h, key: "CH0B", stop: stop, allow: allow})
		}
		if _, err := h.keySize("CHTE"); err == nil {
			stop, allow := smcStopAllow("CHTE")
			out = append(out, &macCtl{label: "SMC CHTE (native)", mode: macModeSwitch, h: h, key: "CHTE", stop: stop, allow: allow})
		}
		for _, k := range []string{"CHIE", "CH0J", "CH0I", "CH0K"} {
			if _, err := h.keySize(k); err != nil {
				continue
			}
			stop, allow := smcStopAllow(k)
			log.Printf("chargectl: warning: %s holds the ceiling by cutting wall power; the pack discharges to %d%% before the adapter returns", k, resumeAt())
			out = append(out, &macCtl{label: "SMC adapter " + k + " (native)", mode: macModeSwitch, h: h, key: k, stop: stop, allow: allow, adapter: true})
			break
		}
		if _, err := h.keySize("CHWA"); err == nil {
			log.Printf("chargectl: warning: CHWA is a fixed 80%% firmware cap; the %d/%d%% window is ignored", pauseAt(), resumeAt())
			stop, allow := smcStopAllow("CHWA")
			out = append(out, &macCtl{label: "SMC CHWA (native, fixed 80%)", mode: macModeSwitch, h: h, key: "CHWA", stop: stop, allow: allow})
		}
	} else {
		log.Printf("chargectl: darwin: native SMC unavailable (%v)", err)
	}
	if p := findTool(bclmBin); p != "" {
		out = append(out, &macCtl{label: "bclm " + p, mode: macModeLevel, bin: p})
	}
	if p := findTool(smcBin); p != "" {
		for _, key := range []string{"CH0B", "CHTE", "CHWA"} {
			if _, ok := smcRead(p, key); !ok {
				continue
			}
			if key == "CHWA" {
				log.Printf("chargectl: warning: CHWA is a fixed 80%% firmware cap; the %d/%d%% window is ignored", pauseAt(), resumeAt())
			}
			stop, allow := smcStopAllow(key)
			out = append(out, &macCtl{label: "smc " + key + " " + p, mode: macModeSwitch, bin: p, key: key, stop: stop, allow: allow})
		}
	}
	return out
}

// hasAllKeys reports whether every SMC key is present and readable (a firmware
// placeholder with zero data size counts as absent).
func hasAllKeys(h *smcHandle, keys ...string) bool {
	for _, k := range keys {
		if _, err := h.keySize(k); err != nil {
			return false
		}
	}
	return true
}

// macReadBattery reads the battery via ioreg, falling back to pmset. "plugged"
// is the OR of every external-power signal (see batteryOnAC).
func macReadBattery() (name string, capacity int, plugged, charging, ok bool) {
	if out, err := runOutput("ioreg", "-rn", "AppleSmartBattery"); err == nil {
		if b := parseIoregBattery(string(out)); b.installed && b.capacity >= 0 {
			return "AppleSmartBattery", b.capacity, batteryOnAC(b), b.charging, true
		}
	}
	if out, err := runOutput("pmset", "-g", "batt"); err == nil {
		if b := parsePmsetBatt(string(out)); b.capacity >= 0 {
			return "InternalBattery", b.capacity, batteryOnAC(b), b.charging, true
		}
	}
	return "", -1, false, false, false
}

// checkMacOnce is one control decision.
func checkMacOnce(ctl *macCtl, st *macLoopState) {
	name, capacity, plugged, charging, ok := macReadBattery()
	if !ok {
		pushError(errors.New("no battery present"))
		return
	}
	if !st.havePlug || plugged != st.lastPlug {
		if plugged {
			log.Printf("chargectl: charger plugged in (battery=%s)", name)
		} else {
			log.Printf("chargectl: charger unplugged (battery=%s)", name)
		}
		st.lastPlug, st.havePlug = plugged, true
	}
	if capacity < 0 {
		pushError(errors.New("battery capacity unreadable"))
		return
	}

	switch ctl.mode {
	case macModeFirmware:
		if err := ctl.writeFirmware(resumeAt(), pauseAt()); err != nil {
			st.writeFails++
			pushError(fmt.Errorf("firmware limit write: %w", err))
			return
		}
		st.writeFails = 0
	case macModeLevel:
		if err := ctl.writeLevel(pauseAt()); err != nil {
			st.writeFails++
			pushError(fmt.Errorf("level write: %w", err))
			return
		}
		st.writeFails = 0
	case macModeSwitch:
		// Adapter mode cuts wall power to hold the ceiling; running on battery
		// there is nothing to cut, so leave the atom alone and wait for AC.
		if ctl.adapter && !plugged {
			st.lastState = -1
			st.warned, st.ineffective = false, 0
			return
		}
		// Adapter mode holds the ceiling by cycling on battery, so widen a
		// narrow band to avoid frequent cut/restore (mirrors batt).
		resume := resumeAt()
		if ctl.adapter && resume < pauseAt()-adapterMinBand {
			resume = pauseAt() - adapterMinBand
		}
		// Stateless hysteresis: derive the wanted state from the capacity and
		// let the lazy write keep it idempotent across restarts.
		desired := -1 // -1: leave as-is
		switch {
		case capacity >= pauseAt():
			desired = 1
		case capacity <= resume:
			desired = 0
		}
		if desired < 0 {
			st.lastState = -1
		} else {
			var err error
			if desired == 1 {
				err = ctl.setCharging(false)
			} else {
				err = ctl.setCharging(true)
			}
			if err != nil {
				st.writeFails++
				pushError(fmt.Errorf("apply: %w", err))
				return
			}
			st.writeFails = 0
			if desired != st.lastState {
				if desired == 1 {
					log.Printf("chargectl: capacity=%d%% >= %d%%: charging stopped", capacity, pauseAt())
				} else {
					log.Printf("chargectl: capacity=%d%% <= %d%%: charging resumed", capacity, resume)
				}
				st.lastState = desired
			}
		}
	default:
		return
	}

	// Effectiveness: on wall power at/above the ceiling, flag the backend when
	// the pack is still climbing (primary) or reports active charging. The trend
	// catches a node that accepts writes but does nothing.
	rising := st.haveCap && capacity > st.lastCap
	st.lastCap, st.haveCap = capacity, true
	if plugged && capacity >= pauseAt() && (rising || charging) {
		st.ineffective++
		if !st.warned {
			log.Printf("chargectl: warning: battery at %d%% (>=%d%%) but still charging via %s; the backend may be ineffective", capacity, pauseAt(), ctl.label)
			st.warned = true
		}
	} else {
		st.warned = false
		st.ineffective = 0
	}
}

// checkMacReadOnly reports the no-backend state once, without spamming.
func checkMacReadOnly(st *macLoopState) {
	_, capacity, plugged, _, ok := macReadBattery()
	if !ok {
		pushError(errors.New("no battery present"))
		return
	}
	if !st.roWarned {
		log.Printf("chargectl: read-only: battery=%d%% plugged=%v; no SMC charge key and no bclm/smc tool available", capacity, plugged)
		st.roWarned = true
	}
}

// writeLevel applies a per-cent limit (BCLM) on the native or tool backend.
func (c *macCtl) writeLevel(level int) error {
	if c.h == nil {
		return runOut(append(sudoArgs(), c.bin, "write", strconv.Itoa(level))...)
	}
	size, err := c.h.keySize("BCLM")
	if err != nil {
		return err
	}
	val := make([]byte, size)
	val[size-1] = byte(level)
	if size >= 2 {
		val[size-2] = byte(level >> 8)
	}
	if err := c.h.writeKeyRaw("BCLM", val); err != nil {
		return err
	}
	// Read back so a write the SMC accepts but drops (or that the firmware
	// immediately reverts) is treated as a failure and triggers fallback.
	if got, err := c.h.readKey("BCLM"); err != nil {
		return fmt.Errorf("BCLM readback: %w", err)
	} else if !bytes.Equal(got, val) {
		return fmt.Errorf("BCLM did not stick: wrote %s, read %s", hexBytes(val), hexBytes(got))
	}
	// Mirror bclm on Intel: also set BFCL (MagSafe LED / "final" level) when the
	// key exists. Cosmetic, so a failure only warns.
	if _, err := c.h.keySize("BFCL"); err == nil {
		if err := c.h.writeKey("BFCL", []byte{bfclValue(level)}); err != nil {
			log.Printf("chargectl: warning: BFCL write failed: %v", err)
		}
	}
	return nil
}

// setCharging writes the stop/allow value for a switch atom. On Apple silicon
// CH0B and CH0C move together.
func (c *macCtl) setCharging(on bool) error {
	val := c.allow
	if !on {
		val = c.stop
	}
	if c.h != nil {
		if c.key == "CH0B" {
			if err := c.h.writeKey("CH0C", val); err != nil {
				return err
			}
		}
		if err := c.h.writeKey(c.key, val); err != nil {
			return err
		}
		if got, err := c.h.readKey(c.key); err != nil {
			return fmt.Errorf("%s readback: %w", c.key, err)
		} else if !bytes.Equal(got, val) {
			return fmt.Errorf("%s did not stick: wrote %s, read %s", c.key, hexBytes(val), hexBytes(got))
		}
		return nil
	}
	hex := hexBytes(val)
	if c.key == "CH0B" {
		if err := runOut(append(sudoArgs(), c.bin, "-k", "CH0C", "-w", hex)...); err != nil {
			return err
		}
	}
	return runOut(append(sudoArgs(), c.bin, "-k", c.key, "-w", hex)...)
}

// writeFirmware sets the macOS 27-era firmware charge window (native only).
func (c *macCtl) writeFirmware(lower, upper int) error {
	if lower < 1 {
		lower = 1
	}
	if upper > 100 {
		upper = 100
	}
	if lower >= upper {
		return fmt.Errorf("invalid firmware window %d..%d", lower, upper)
	}
	return c.h.writeFirmware(lower, upper)
}

// smcRead reads an SMC atom through the external tool; ok=false when it errors
// or the atom is absent.
func smcRead(path, key string) (string, bool) {
	out, err := runOutput(path, "-k", key, "-r")
	if err != nil {
		return "", false
	}
	f := strings.Fields(string(out))
	if len(f) < 3 {
		return "", false
	}
	return f[2], true
}

// runOut runs argv, folding its output into the error for logging.
func runOut(argv ...string) error {
	if len(argv) == 0 {
		return errors.New("empty command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(argv, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
