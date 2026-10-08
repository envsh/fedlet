//go:build darwin

// batt_darwin.go — battery reading through the IOKit registry.
//
// `ioreg -n AppleSmartBattery -r -a` dumps the built-in battery as an XML
// plist, which is the only interface on macOS that exposes both the design
// capacity and the charge cycle count in one read (IOKit's
// IOPSCopyPowerSourcesInfo, by contrast, reports neither).
//
// Unit conversions, all taken from the driver's raw output:
//
//	AppleRawCurrentCapacity / AppleRawMaxCapacity / DesignCapacity → mWh
//	Voltage      → mV    → V
//	Amperage     → mA    → mW via mA × V
//	Temperature  → 0.1 K → °C via K/10 − 273.15
package sysinfo

import (
	"fmt"
	"math"
	"os/exec"

	"howett.net/plist"
)

// ioregBattery mirrors the AppleSmartBattery dictionary. Field names match the
// registry keys exactly, so no plist tag is required.
type ioregBattery struct {
	AppleRawCurrentCapacity float64
	AppleRawMaxCapacity     float64
	CurrentCapacity         float64
	MaxCapacity             float64
	DesignCapacity          float64
	Voltage                 float64
	Amperage                float64
	CycleCount              float64
	Temperature             float64
	IsCharging              bool
	ExternalConnected       bool
	FullyCharged            bool
	PermanentFailureStatus  float64
}

// readBatteries runs ioreg and decodes its plist output.
func readBatteries(errs *errCollector) ([]BatteryInfo, error) {
	out, err := exec.Command("ioreg", "-n", "AppleSmartBattery", "-r", "-a").Output()
	if err != nil {
		return nil, fmt.Errorf("ioreg AppleSmartBattery: %w", err)
	}
	raws, err := decodeIoregPlist(out)
	if err != nil {
		return nil, err
	}
	bats := make([]BatteryInfo, 0, len(raws))
	for _, raw := range raws {
		bats = append(bats, convertIoreg(raw))
	}
	return bats, nil
}

// decodeIoregPlist accepts both shapes ioreg emits: an array (the -a flag) and
// a bare dict, which older Intel machines still produce.
func decodeIoregPlist(out []byte) ([]ioregBattery, error) {
	var list []ioregBattery
	if _, err := plist.Unmarshal(out, &list); err == nil && len(list) > 0 {
		return list, nil
	}
	var one ioregBattery
	if _, err := plist.Unmarshal(out, &one); err != nil {
		return nil, fmt.Errorf("cannot decode ioreg plist: %w", err)
	}
	return []ioregBattery{one}, nil
}

// convertIoreg maps one ioreg entry onto the shared schema, applying rule B
// individually so an unsupported field (a battery with no cycle counter, for
// instance) does not invalidate the rest of the reading.
func convertIoreg(raw ioregBattery) BatteryInfo {
	b := invalidBattery()

	// Capacity: prefer the energy readings, fall back to the generic pair.
	full := raw.AppleRawMaxCapacity
	current := raw.AppleRawCurrentCapacity
	if full <= 0 {
		full = raw.MaxCapacity
		current = raw.CurrentCapacity
	}
	b.Full = guardNum(full)
	b.Current = guardNum(current)
	b.Design = guardNum(raw.DesignCapacity)
	b.HealthPercent = healthPercent(raw.AppleRawMaxCapacity, raw.DesignCapacity)
	if full > 0 {
		b.Percent = guardPct(current, full)
	}

	b.Cycles = guardNum(raw.CycleCount)
	if raw.Voltage > 0 {
		b.Voltage = raw.Voltage / 1000 // mV → V
	}
	// mA × V = mW; negative amperage means the pack is discharging.
	if raw.Amperage != 0 && raw.Voltage > 0 {
		b.ChargeRate = guardNum(math.Abs(raw.Amperage) * (raw.Voltage / 1000))
		if !raw.IsCharging {
			b.ChargeRate = -b.ChargeRate
		}
	}
	if c := raw.Temperature/10 - 273.15; c > -100 && c < 150 {
		b.Temperature = c
	}

	b.Present = TriYes
	b.Charging = tri(true, raw.IsCharging)
	b.ACPower = tri(true, raw.ExternalConnected)
	b.State = ioregState(raw)
	if raw.PermanentFailureStatus != 0 {
		b.Health = "Failed"
	}
	return b
}

// ioregState collapses the three booleans into the same vocabulary the other
// platforms emit.
func ioregState(raw ioregBattery) string {
	switch {
	case raw.FullyCharged:
		return "Full"
	case raw.IsCharging:
		return "Charging"
	case !raw.ExternalConnected:
		return "Discharging"
	default:
		return "Idle"
	}
}
