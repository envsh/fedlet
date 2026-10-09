package sysinfo

import (
	"fmt"
	"math"

	"howett.net/plist"
)

// ioregNum is a plist number that accepts both <integer> and <real>.
//
// howett.net/plist decodes <integer> into a *cfNumber but only assigns it to
// int/uint fields, so a plain float64 field aborts with "type mismatch: tried
// to decode plist type `integer' into value of type `float64'". ioreg always
// publishes these registry values as <integer>, so plain float64 never worked.
type ioregNum float64

func (n *ioregNum) UnmarshalPlist(unmarshal func(interface{}) error) error {
	var i int64
	if err := unmarshal(&i); err == nil {
		*n = ioregNum(i)
		return nil
	}
	var f float64
	if err := unmarshal(&f); err != nil {
		return err
	}
	*n = ioregNum(f)
	return nil
}

// ioregBattery mirrors the AppleSmartBattery dictionary. Field names match the
// registry keys exactly, so no plist tag is required. Numeric fields use
// ioregNum because the registry reports them as integers (see above).
type ioregBattery struct {
	AppleRawCurrentCapacity ioregNum
	AppleRawMaxCapacity     ioregNum
	CurrentCapacity         ioregNum
	MaxCapacity             ioregNum
	DesignCapacity          ioregNum
	NominalChargeCapacity   ioregNum
	Voltage                 ioregNum
	Amperage                ioregNum
	CycleCount              ioregNum
	Temperature             ioregNum
	IsCharging              bool
	ExternalConnected       bool
	FullyCharged            bool
	PermanentFailureStatus  ioregNum
	BatteryData             ioregBatteryData
}

// ioregBatteryData is the nested gauge blob. Newer macOS releases (and the
// Apple silicon/Intel split in general) drift which keys live at the top level;
// from macOS 26 the capacity figures exist only here. The same names reappear
// nested, so they are decoded separately rather than shadowing the outer keys.
type ioregBatteryData struct {
	CycleCount            ioregNum
	DesignCapacity        ioregNum
	RemainingCapacity     ioregNum
	NominalChargeCapacity ioregNum
	FullChargeCapacity    ioregNum
}

// decodeIoregPlist accepts both shapes ioreg emits: an array (the -a flag, all
// known macOS versions) and a bare dict (older Intel machines).
func decodeIoregPlist(out []byte) ([]ioregBattery, error) {
	var list []ioregBattery
	_, listErr := plist.Unmarshal(out, &list)
	if listErr == nil && len(list) > 0 {
		return list, nil
	}

	var one ioregBattery
	if _, err := plist.Unmarshal(out, &one); err == nil {
		return []ioregBattery{one}, nil
	}

	// Report the array error (the real one) instead of the misleading
	// "array into struct" error produced by the fallback attempt.
	if listErr != nil {
		return nil, fmt.Errorf("cannot decode ioreg plist: %w", listErr)
	}
	return nil, fmt.Errorf("cannot decode ioreg plist: no battery entries")
}

// convertIoreg maps one ioreg entry onto the shared schema, applying rule B
// individually so an unsupported field (a battery with no cycle counter, for
// instance) does not invalidate the rest of the reading.
//
// Units: capacity/design → mAh, Voltage → mV, Amperage → mA (mW via mA × V),
// Temperature → decikelvin (K/10 − 273.15).
func convertIoreg(raw ioregBattery) BatteryInfo {
	b := invalidBattery()

	bd := raw.BatteryData

	// Full charge: the top-level raw reading, then the nested gauge blob, then
	// the legacy MaxCapacity key last. MaxCapacity holds milliamp-hours on
	// Intel but a service flag (100) on Apple silicon, so reaching it is a
	// fallback of last resort rather than a first choice.
	full := float64(raw.AppleRawMaxCapacity)
	if full <= 0 {
		full = float64(bd.NominalChargeCapacity)
	}
	if full <= 0 {
		full = float64(bd.FullChargeCapacity)
	}
	if full <= 0 {
		full = float64(raw.NominalChargeCapacity)
	}
	if full <= 0 {
		full = float64(raw.MaxCapacity)
	}

	current := float64(raw.AppleRawCurrentCapacity)
	if current <= 0 {
		current = float64(bd.RemainingCapacity)
	}
	if current <= 0 {
		current = float64(raw.CurrentCapacity)
	}

	design := float64(raw.DesignCapacity)
	if design <= 0 {
		design = float64(bd.DesignCapacity)
	}

	cycles := float64(raw.CycleCount)
	if cycles <= 0 {
		cycles = float64(bd.CycleCount)
	}

	vol := float64(raw.Voltage)
	amp := float64(raw.Amperage)
	temp := float64(raw.Temperature)

	// A capacity/cycle value of zero means the key was absent, not that the
	// battery is empty, so it stays at the -1 sentinel (rule B). Percent is
	// guarded separately because a genuine 0 % reading is meaningful.
	if full > 0 {
		b.Full = guardNum(full)
	}
	if current > 0 {
		b.Current = guardNum(current)
	}
	if design > 0 {
		b.Design = guardNum(design)
	}
	if cycles > 0 {
		b.Cycles = guardNum(cycles)
	}
	b.HealthPercent = healthPercent(full, design)
	if full > 0 {
		b.Percent = guardPct(current, full)
	}
	if vol > 0 {
		b.Voltage = vol / 1000 // mV → V
	}
	// mA × V = mW; negative amperage means the pack is discharging.
	if amp != 0 && vol > 0 {
		b.ChargeRate = guardNum(math.Abs(amp) * (vol / 1000))
		if !raw.IsCharging {
			b.ChargeRate = -b.ChargeRate
		}
	}
	if c := temp/10 - 273.15; c > -100 && c < 150 {
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
