//go:build linux || android

// batt_linux.go — battery reading through sysfs, with a dumpsys fallback for
// Android.
//
// sysfs is the canonical source on Linux: /sys/class/power_supply/<dev>/ holds
// per-battery attributes. Energy attributes (energy_*) are already in µWh and
// convert directly to mWh; charge attributes (charge_*) are in µAh and are
// converted through the measured voltage, because capacity in mAh alone cannot
// be compared with a mWh design figure.
//
// On Android the same tree exists but SELinux frequently denies access to
// unprivileged readers, so an unreadable or empty tree falls back to
// `/system/bin/dumpsys battery`, which has no cycle counter at all — Cycles is
// therefore -1 there (rule B), never a fabricated 0.
package sysinfo

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// powerSupplyDir is where the kernel publishes supply devices. It is a var
// rather than a const only so tests can point it at a fixture tree.
var powerSupplyDir = "/sys/class/power_supply"

// readBatteries enumerates every Battery-typed power supply and every supply
// that can power the machine from outside (Mains/USB/Wireless/USB-C).
func readBatteries(errs *errCollector) ([]BatteryInfo, error) {
	entries, err := os.ReadDir(powerSupplyDir)
	if err != nil {
		if runtime.GOOS == "android" {
			return readDumpsysBattery(errs)
		}
		return nil, fmt.Errorf("cannot read %s: %w", powerSupplyDir, err)
	}

	var bats []BatteryInfo
	acKnown, acOnline := false, false

	for _, e := range entries {
		dir := filepath.Join(powerSupplyDir, e.Name())
		typ, ok := readSysfsString(dir, "type")
		if !ok {
			continue
		}
		switch typ {
		case "Battery":
			if b, ok := readSysfsBattery(dir, e.Name(), errs); ok {
				bats = append(bats, b)
			}
		case "Mains", "USB", "USB_C", "Wireless":
			acKnown = true
			if v, ok := readSysfsInt(dir, "online"); ok && v > 0 {
				acOnline = true
			}
		}
	}

	// An Android build whose sysfs tree is present but empty is exactly the
	// SELinux case, so it still gets the dumpsys fallback.
	if len(bats) == 0 && runtime.GOOS == "android" {
		if dbats, derr := readDumpsysBattery(errs); derr == nil && len(dbats) > 0 {
			return dbats, nil
		}
	}

	for i := range bats {
		if acKnown {
			bats[i].ACPower = tri(true, acOnline)
		}
	}
	return bats, nil
}

// readSysfsBattery builds one BatteryInfo from a single power supply directory.
// ok=false means "not a battery we should publish" (an empty removable bay),
// which is neither an error nor a row. Every attribute is optional: kernels
// and battery drivers disagree about what they expose, so a missing attribute
// becomes -1 (rule B) rather than a failure.
func readSysfsBattery(dir, name string, errs *errCollector) (BatteryInfo, bool) {
	if v, ok := readSysfsInt(dir, "present"); ok && v == 0 {
		return BatteryInfo{}, false
	}

	b := invalidBattery()

	if status, ok := readSysfsString(dir, "status"); ok {
		b.State = normalizeState(status)
		b.Charging = chargingTri(status)
	}

	// Full / design / current capacity, all normalised to mWh.
	full := readCapacityMWh(dir, "energy_full", "charge_full")
	design := readCapacityMWh(dir, "energy_full_design", "charge_full_design")
	current := readCapacityMWh(dir, "energy_now", "charge_now")
	b.Full = full
	b.Design = design
	b.Current = current
	b.HealthPercent = healthPercent(full, design)

	if v, ok := readSysfsInt(dir, "capacity"); ok && v >= 0 {
		b.Percent = float64(v)
	} else if full > 0 {
		// No percentage attribute, but the mWh figures let us derive it.
		b.Percent = guardPct(current, full)
	} else {
		// Rule B: neither an absolute level nor a computable ratio.
		errs.Add("battery:"+name, "no capacity attribute")
	}

	if v, ok := readSysfsInt(dir, "cycle_count"); ok && v >= 0 {
		b.Cycles = float64(v)
	}
	if v, ok := readSysfsInt(dir, "voltage_now"); ok && v >= 0 {
		b.Voltage = float64(v) / 1e6 // µV → V
	}
	b.ChargeRate = readChargeRateMw(dir, b.Charging == TriYes)
	if v, ok := readSysfsInt(dir, "temperature"); ok && v > 0 {
		b.Temperature = float64(v) / 10 // 0.1 °C → °C
	}
	if h, ok := readSysfsString(dir, "health"); ok {
		b.Health = h
	}

	return b, true
}

// readCapacityMWh resolves an attribute pair into mWh: the energy_* form when
// present, otherwise charge_* scaled by the measured voltage. InvalidNum when
// neither is readable or the scale factor is missing.
func readCapacityMWh(dir, energyFile, chargeFile string) float64 {
	if v, ok := readSysfsInt(dir, energyFile); ok && v >= 0 {
		return float64(v) / 1000 // µWh → mWh
	}
	if v, ok := readSysfsInt(dir, chargeFile); ok && v >= 0 {
		if volt, ok := readSysfsInt(dir, "voltage_now"); ok && volt > 0 {
			// µAh × µV × 1e-9 = mWh
			return float64(v) * float64(volt) * 1e-9
		}
	}
	return InvalidNum
}

// readChargeRateMw returns charging as a positive and discharging as a negative
// milliwatt figure, or -1 when the kernel exposes no power/current attribute.
func readChargeRateMw(dir string, charging bool) float64 {
	sign := -1.0
	if charging {
		sign = 1.0
	}
	if v, ok := readSysfsInt(dir, "power_now"); ok && v >= 0 {
		return float64(v) / 1000 * sign // µW → mW
	}
	if cur, ok := readSysfsInt(dir, "current_now"); ok && cur >= 0 {
		if volt, ok := readSysfsInt(dir, "voltage_now"); ok && volt > 0 {
			return float64(cur) * float64(volt) * 1e-9 * sign // µA × µV → mW
		}
	}
	return InvalidNum
}

// chargingTri maps the kernel's status string onto the tri-state. An explicit
// "unknown" stays unknown rather than collapsing into "no", because the driver
// is telling us it has no opinion yet.
func chargingTri(status string) int {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "charging":
		return TriYes
	case "discharging", "full", "not charging", "idle":
		return TriNo
	default: // "unknown", "", anything unrecognised
		return TriUnknown
	}
}

// normalizeState keeps the kernel wording but tidies "Not charging" into a
// single token so consumers can switch on it.
func normalizeState(status string) string {
	s := strings.TrimSpace(status)
	if strings.EqualFold(s, "not charging") {
		return "Idle"
	}
	return s
}

// readSysfsInt parses a single integer attribute, reporting ok=false when the
// file is absent, unreadable or not numeric.
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

// readDumpsysBattery is the Android-only fallback. dumpsys exposes charge
// level, voltage, temperature and health but no cycle counter and no design
// capacity, so those stay at -1 (rule B) instead of a made-up zero.
func readDumpsysBattery(errs *errCollector) ([]BatteryInfo, error) {
	out, err := exec.Command("/system/bin/dumpsys", "battery").Output()
	if err != nil {
		return nil, fmt.Errorf("dumpsys battery failed: %w", err)
	}

	fields := parseDumpsysBattery(string(out))
	if len(fields) == 0 {
		return nil, fmt.Errorf("dumpsys battery returned no data")
	}
	if v, ok := fields["present"]; ok && v == "false" {
		return nil, nil
	}

	b := invalidBattery()
	if v, ok := fields["level"]; ok {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			b.Percent = guardNum(n)
		}
	}
	if v, ok := fields["voltage"]; ok {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			b.Voltage = guardNum(n / 1000) // mV → V
		}
	}
	if v, ok := fields["temperature"]; ok {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			b.Temperature = guardNum(n / 10) // 0.1 °C → °C
		}
	}
	if v, ok := fields["health"]; ok {
		b.Health = mapAndroidHealth(v)
	}
	if v, ok := fields["status"]; ok {
		st := mapAndroidStatus(v)
		b.State = st
		b.Charging = chargingTri(st)
	}
	for _, key := range []string{"ac powered", "usb powered", "wireless powered"} {
		if v, ok := fields[key]; ok {
			b.ACPower = tri(true, v == "true")
			break
		}
	}

	// dumpsys has no cycle counter; try sysfs for that one attribute in case
	// only it is readable, otherwise leave the -1 sentinel.
	if v, ok := readSysfsInt(filepath.Join(powerSupplyDir, "battery"), "cycle_count"); ok && v >= 0 {
		b.Cycles = float64(v)
	}

	errs.Add("battery", "dumpsys fallback: no design capacity / cycle count available")
	return []BatteryInfo{b}, nil
}

// parseDumpsysBattery turns "  key: value" lines into a lowercase-key map.
func parseDumpsysBattery(text string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return out
}

// mapAndroidStatus converts dumpsys status codes into the same wording sysfs
// uses, so consumers see one vocabulary on both paths.
func mapAndroidStatus(v string) string {
	switch strings.TrimSpace(v) {
	case "1":
		return "Unknown"
	case "2":
		return "Charging"
	case "3":
		return "Discharging"
	case "4":
		return "Idle"
	case "5":
		return "Full"
	default:
		return v
	}
}

// mapAndroidHealth maps dumpsys health codes onto a readable label.
func mapAndroidHealth(v string) string {
	switch strings.TrimSpace(v) {
	case "1":
		return "Unknown"
	case "2":
		return "Good"
	case "3":
		return "Overheat"
	case "4":
		return "Dead"
	case "5":
		return "Over voltage"
	case "6":
		return "Unspecified failure"
	case "7":
		return "Cold"
	default:
		return v
	}
}
