//go:build linux || android

package chargectl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// sandbox points sysRoot at a fresh temp tree and returns the fixture
// power_supply directory inside it.
func sandbox(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	oldRoot, oldDir, oldDelay, oldPref := sysRoot, powerSupplyDir, writeRetryDelay, preferACC
	sysRoot = root
	powerSupplyDir = "/sys/class/power_supply"
	writeRetryDelay = 0
	t.Cleanup(func() {
		sysRoot, powerSupplyDir, writeRetryDelay, preferACC = oldRoot, oldDir, oldDelay, oldPref
	})
	ps := filepath.Join(root, "class", "power_supply")
	if err := os.MkdirAll(ps, 0o755); err != nil {
		t.Fatal(err)
	}
	return ps
}

func writeAttr(t *testing.T, path, val string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(val), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readAttr(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func resetErrors() {
	statusLastErrsMu.Lock()
	statusLastErrs = [3]error{}
	statusLastErrsMu.Unlock()
}

func TestProbePrefersThreshold(t *testing.T) {
	ps := sandbox(t)
	bat := filepath.Join(ps, "BAT0")
	writeAttr(t, filepath.Join(bat, "type"), "Battery")
	writeAttr(t, filepath.Join(bat, "charge_control_start_threshold"), "0")
	writeAttr(t, filepath.Join(bat, "charge_control_end_threshold"), "100")
	writeAttr(t, filepath.Join(bat, "store_mode"), "0")

	preferACC = false
	ctl := probeController()
	if ctl == nil {
		t.Fatal("expected a controller")
	}
	if ctl.mode != modeThreshold {
		t.Fatalf("mode=%s, want threshold", ctl.modeName())
	}
	if !strings.HasSuffix(ctl.endPath, "charge_control_end_threshold") {
		t.Fatalf("endPath=%s", ctl.endPath)
	}

	if err := ctl.ensureThreshold(); err != nil {
		t.Fatalf("ensureThreshold: %v", err)
	}
	if got := readAttr(t, ctl.endPath); got != "75" {
		t.Fatalf("end threshold=%s, want 75", got)
	}
	if got := readAttr(t, ctl.startPath); got != "30" {
		t.Fatalf("start threshold=%s, want 30", got)
	}
}

func TestProbeFallsBackToSwitch(t *testing.T) {
	ps := sandbox(t)
	bat := filepath.Join(ps, "battery")
	writeAttr(t, filepath.Join(bat, "type"), "Battery")
	writeAttr(t, filepath.Join(bat, "store_mode"), "0")

	preferACC = true
	ctl := probeController()
	if ctl == nil {
		t.Fatal("expected a controller")
	}
	if ctl.mode != modeSwitch {
		t.Fatalf("mode=%s, want switch", ctl.modeName())
	}
	if !strings.HasSuffix(ctl.switchPath, "battery/store_mode") {
		t.Fatalf("switchPath=%s", ctl.switchPath)
	}
	if ctl.enableVal != "0" || ctl.disableVal != "1" {
		t.Fatalf("enable=%s disable=%s, want 0/1", ctl.enableVal, ctl.disableVal)
	}
}

func TestWriteAtCapacity(t *testing.T) {
	sandbox(t)
	node := "/sys/devices/platform/google,charger/charge_stop_level"
	full := filepath.Join(sysRoot, strings.TrimPrefix(node, "/sys/"))
	writeAttr(t, full, "100")

	preferACC = true
	ctl := probeController()
	if ctl == nil {
		t.Fatal("expected a controller")
	}
	if ctl.disableVal != atCapacity {
		t.Fatalf("disableVal=%s, want %s", ctl.disableVal, atCapacity)
	}
	if err := ctl.stopCharging(76); err != nil {
		t.Fatalf("stopCharging: %v", err)
	}
	if got := readAttr(t, full); got != "76" {
		t.Fatalf("node=%s, want 76", got)
	}
	if err := ctl.startCharging(); err != nil {
		t.Fatalf("startCharging: %v", err)
	}
	if got := readAttr(t, full); got != "100" {
		t.Fatalf("node=%s, want 100", got)
	}
}

func TestHysteresis(t *testing.T) {
	ps := sandbox(t)
	bat := filepath.Join(ps, "battery")
	writeAttr(t, filepath.Join(bat, "type"), "Battery")
	writeAttr(t, filepath.Join(bat, "present"), "1")
	capPath := filepath.Join(bat, "capacity")
	writeAttr(t, capPath, "50")
	writeAttr(t, filepath.Join(bat, "store_mode"), "0")
	writeAttr(t, filepath.Join(ps, "AC", "type"), "Mains")
	writeAttr(t, filepath.Join(ps, "AC", "online"), "1")

	preferACC = true
	ctl := probeController()
	if ctl == nil {
		t.Fatal("expected a controller")
	}
	st := &loopState{}

	checkOnce(ctl, st) // 50%, no action
	if got := readAttr(t, ctl.switchPath); got != "0" {
		t.Fatalf("after 50%%: store_mode=%s, want 0", got)
	}

	writeAttr(t, capPath, "76")
	checkOnce(ctl, st)
	if got := readAttr(t, ctl.switchPath); got != "1" {
		t.Fatalf("after 76%%: store_mode=%s, want 1", got)
	}
	if !st.paused {
		t.Fatal("expected paused")
	}

	checkOnce(ctl, st) // still 76%, must stay paused
	if got := readAttr(t, ctl.switchPath); got != "1" {
		t.Fatalf("still 76%%: store_mode=%s, want 1", got)
	}

	writeAttr(t, capPath, "20")
	checkOnce(ctl, st)
	if got := readAttr(t, ctl.switchPath); got != "0" {
		t.Fatalf("after 20%%: store_mode=%s, want 0", got)
	}
	if st.paused {
		t.Fatal("expected resumed")
	}
}

func TestPlugUnplug(t *testing.T) {
	ps := sandbox(t)
	bat := filepath.Join(ps, "battery")
	writeAttr(t, filepath.Join(bat, "type"), "Battery")
	writeAttr(t, filepath.Join(bat, "present"), "1")
	capPath := filepath.Join(bat, "capacity")
	writeAttr(t, capPath, "76")
	writeAttr(t, filepath.Join(bat, "store_mode"), "0")
	onlinePath := filepath.Join(ps, "AC", "online")
	writeAttr(t, filepath.Join(ps, "AC", "type"), "Mains")
	writeAttr(t, onlinePath, "1")

	preferACC = true
	ctl := probeController()
	if ctl == nil {
		t.Fatal("expected a controller")
	}
	st := &loopState{}

	checkOnce(ctl, st) // plugged, 76% -> pause
	if got := readAttr(t, ctl.switchPath); got != "1" {
		t.Fatalf("plugged: store_mode=%s, want 1", got)
	}

	writeAttr(t, onlinePath, "0")
	checkOnce(ctl, st) // unplugged, no change
	if got := readAttr(t, ctl.switchPath); got != "1" {
		t.Fatalf("unplugged: store_mode=%s, want 1", got)
	}

	writeAttr(t, capPath, "20")
	writeAttr(t, onlinePath, "1")
	checkOnce(ctl, st) // replugged below resume -> resume
	if got := readAttr(t, ctl.switchPath); got != "0" {
		t.Fatalf("replugged: store_mode=%s, want 0", got)
	}
	if st.paused {
		t.Fatal("expected resumed")
	}
}

func TestNoBatteryIdle(t *testing.T) {
	ps := sandbox(t)
	writeAttr(t, filepath.Join(ps, "AC", "type"), "Mains")
	writeAttr(t, filepath.Join(ps, "AC", "online"), "1")

	resetErrors()
	checkOnce(&controller{mode: modeSwitch}, &loopState{})
	if len(LastErrs()) == 0 {
		t.Fatal("expected an error when no battery is present")
	}
}

func TestProbeAccess(t *testing.T) {
	sandbox(t)
	p := filepath.Join(t.TempDir(), "node")
	writeAttr(t, p, "0")
	if err := os.Chmod(p, 0o444); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if !probeAccess(p) {
			t.Fatal("root should always have access")
		}
	} else if probeAccess(p) {
		t.Fatal("read-only node should not be writable")
	}
	if got := errReason(unix.EACCES); !strings.Contains(got, "EACCES") {
		t.Fatalf("errReason(EACCES)=%q", got)
	}
	if got := errReason(nil); got != "ok" {
		t.Fatalf("errReason(nil)=%q", got)
	}
}
