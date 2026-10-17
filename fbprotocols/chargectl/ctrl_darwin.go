//go:build darwin

package chargectl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// macOS exposes no /sys power-supply tree; the SMC charge atoms and BCLM are
// only reachable through IOKit. We deliberately do NOT link IOKit via cgo: cgo
// needs the macOS SDK at build time and breaks `go build` / cross-compilation
// from Linux and CI, while buying nothing (the root/entitlement dance is the
// same either way). Instead we shell out to the two de-facto tools — `bclm`
// (Intel BCLM level) and `smc` (Apple silicon SMC atoms) — and run read-only
// when neither exists. Reads use ioreg (falling back to pmset) and need no root.
//
// Caveat: recent macOS increasingly rejects these writes even as root — BCLM is
// a no-op on macOS >= 15.0 without SIP disabled, and CH0B/CH0C/CHTE return
// kIOReturnNotPrivileged on 26.7+/27 beta. The loop then keeps running
// read-only and checkMacOnce warns once. The smc/bclm command syntax below is
// reconstructed from public reports and has NOT been verified on real hardware.

var (
	smcBin  = envOr("FEDLET_CHARGECTL_SMC", "smc")
	bclmBin = envOr("FEDLET_CHARGECTL_BCLM", "bclm")
	macMode = os.Getenv("FEDLET_CHARGECTL_MAC_MODE") // auto|level|switch
)

func envOr(k, dft string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return dft
}

// sudoArgs is an optional privilege prefix. It is empty by default because
// fedbridge is expected to run as root; set FEDLET_CHARGECTL_SUDO (e.g.
// "sudo -n") only when running unprivileged.
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

// macCtl is a resolved macOS charge-control backend.
type macCtl struct {
	kind     string // "bclm" | "smc" | "readonly"
	label    string
	bin      string // resolved tool path
	key      string // smc atom (CH0B / CHTE / CHWA)
	stopVal  string
	allowVal string
}

type macLoopState struct {
	lastState  int
	lastPlug   bool
	havePlug   bool
	warned     bool
	roWarned   bool
	writeFails int
}

func runMacLoop(stop <-chan struct{}) {
	logf("start(darwin): pause>=%d%% resume<=%d%% interval=%s", pauseAt(), resumeAt(), Interval)
	defer logf("stopped")
	ctl := macProbeController()
	logf("darwin: using %s", ctl.label)
	st := &macLoopState{}
	checkMacOnce(ctl, st)
	ticker := time.NewTicker(Interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if st.writeFails >= reprobeAfter {
				if nc := macProbeController(); nc.kind != "readonly" {
					logf("re-probing after %d failed writes (now %s)", st.writeFails, nc.label)
					ctl = nc
				}
				st.writeFails = 0
			}
			checkMacOnce(ctl, st)
		}
	}
}

func checkMacOnce(ctl *macCtl, st *macLoopState) {
	name, capacity, plugged, charging, ok := macReadBattery()
	if !ok {
		pushError(errors.New("no battery present"))
		return
	}
	if !st.havePlug || plugged != st.lastPlug {
		if plugged {
			logf("charger plugged in (battery=%s)", name)
		} else {
			logf("charger unplugged (battery=%s)", name)
		}
		st.lastPlug, st.havePlug = plugged, true
	}

	if !plugged || capacity < pauseAt() {
		st.warned = false
	}

	switch ctl.kind {
	case "bclm":
		if err := ctl.bclmWrite(pauseAt()); err != nil {
			st.writeFails++
			pushError(fmt.Errorf("bclm write: %w", err))
		} else {
			st.writeFails = 0
		}
	case "smc":
		if !plugged {
			return
		}
		desired := -1
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
			pushError(fmt.Errorf("smc apply: %w", err))
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
	default:
		if !st.roWarned {
			logf("read-only: battery=%d%% plugged=%v; install bclm or smc (see README) to enable charge control", capacity, plugged)
			st.roWarned = true
		}
		return
	}

	if plugged && capacity >= pauseAt() && charging && !st.warned {
		logf("warning: battery at %d%% (>=%d%%) but still charging via %s; the backend may be ineffective", capacity, pauseAt(), ctl.label)
		st.warned = true
	}
}

func macProbeController() *macCtl {
	ctl := macDetect()
	if ctl.kind == "readonly" {
		setAuthStatus("read-only")
	} else {
		setAuthStatus(authStatusLocal)
	}
	return ctl
}

// macDetect resolves a backend from the installed tools and
// FEDLET_CHARGECTL_MAC_MODE (auto|level|switch).
func macDetect() *macCtl {
	bclmPath, bclmErr := exec.LookPath(bclmBin)
	smcPath, smcErr := exec.LookPath(smcBin)
	haveBclm := bclmErr == nil
	haveSmc := smcErr == nil
	switch macMode {
	case "level":
		if haveBclm {
			return &macCtl{kind: "bclm", label: bclmPath, bin: bclmPath}
		}
		return &macCtl{kind: "readonly", label: "read-only (bclm not found)"}
	case "switch":
		if haveSmc {
			return macSMSCtrl(smcPath)
		}
		return &macCtl{kind: "readonly", label: "read-only (smc not found)"}
	case "", "auto":
	default:
		logf("unknown FEDLET_CHARGECTL_MAC_MODE=%q; using auto", macMode)
	}
	if haveBclm {
		return &macCtl{kind: "bclm", label: bclmPath, bin: bclmPath}
	}
	if haveSmc {
		return macSMSCtrl(smcPath)
	}
	return &macCtl{kind: "readonly", label: "read-only (need bclm or smc)"}
}

// macSMSCtrl picks the SMC charge atom for this machine. Apple silicon exposes
// "CH0B"/"CH0C" (inhibit bits) or, on Tahoe-era firmware, "CHTE"; "CHWA" is a
// firmware 80% cap present on some models and is the last resort.
func macSMSCtrl(path string) *macCtl {
	if _, ok := smcRead(path, "CH0B"); ok {
		return &macCtl{kind: "smc", label: "smc CH0B/CH0C", bin: path, key: "CH0B", stopVal: "02", allowVal: "00"}
	}
	if _, ok := smcRead(path, "CHTE"); ok {
		return &macCtl{kind: "smc", label: "smc CHTE", bin: path, key: "CHTE", stopVal: "01000000", allowVal: "00000000"}
	}
	if _, ok := smcRead(path, "CHWA"); ok {
		logf("warning: CHWA is a fixed 80%% firmware cap; the %d/%d%% window is ignored", pauseAt(), resumeAt())
		return &macCtl{kind: "smc", label: "smc CHWA (fixed 80%)", bin: path, key: "CHWA", stopVal: "01", allowVal: "00"}
	}
	return &macCtl{kind: "readonly", label: "read-only (no known SMC atom)"}
}

func macReadBattery() (name string, capacity int, plugged, charging, ok bool) {
	if out, err := runOutput("ioreg", "-rn", "AppleSmartBattery"); err == nil {
		if b := parseIoregBattery(string(out)); b.installed && b.capacity >= 0 {
			return "AppleSmartBattery", b.capacity, b.externalPower, b.charging, true
		}
	}
	if out, err := runOutput("pmset", "-g", "batt"); err == nil {
		if b := parsePmsetBatt(string(out)); b.capacity >= 0 {
			return "InternalBattery", b.capacity, b.externalPower, b.charging, true
		}
	}
	return "", -1, false, false, false
}

func (c *macCtl) bclmWrite(level int) error {
	return runOut(append(sudoArgs(), c.bin, "write", strconv.Itoa(level))...)
}

// stopCharging writes the stop value; on Apple silicon CH0B and CH0C move
// together.
func (c *macCtl) stopCharging() error {
	if c.key == "CH0B" {
		if err := runOut(append(sudoArgs(), c.bin, "-k", "CH0C", "-w", c.stopVal)...); err != nil {
			return err
		}
	}
	return runOut(append(sudoArgs(), c.bin, "-k", c.key, "-w", c.stopVal)...)
}

func (c *macCtl) startCharging() error {
	if c.key == "CH0B" {
		if err := runOut(append(sudoArgs(), c.bin, "-k", "CH0C", "-w", c.allowVal)...); err != nil {
			return err
		}
	}
	return runOut(append(sudoArgs(), c.bin, "-k", c.key, "-w", c.allowVal)...)
}

// smcRead reads an SMC atom; ok=false when the tool errors or the atom is absent.
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
