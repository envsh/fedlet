//go:build windows

package sysinfo

import (
	"testing"
)

func TestApplyWindowsInfoAbsoluteCapacities(t *testing.T) {
	b := invalidBattery()
	applyWindowsInfo(&b, batteryInformation{
		Capabilities:        0,
		DesignedCapacity:    60000,
		FullChargedCapacity: 50000,
		CycleCount:          456,
	})

	if b.Design != 60000 || b.Full != 50000 {
		t.Errorf("Design/Full = %v/%v, want 60000/50000", b.Design, b.Full)
	}
	if b.Cycles != 456 {
		t.Errorf("Cycles = %v, want 456 (the field upstream never copies out)", b.Cycles)
	}
	// 50000/60000 = 83.33%
	if b.HealthPercent < 83.3 || b.HealthPercent > 83.4 {
		t.Errorf("HealthPercent = %v, want ~83.33", b.HealthPercent)
	}
}

func TestApplyWindowsInfoRelativeCapacities(t *testing.T) {
	b := invalidBattery()
	applyWindowsInfo(&b, batteryInformation{
		Capabilities:        batteryCapacityRelative,
		DesignedCapacity:    100,
		FullChargedCapacity: 90,
		CycleCount:          12,
	})

	// Relative values are not mWh, so they must not masquerade as absolute
	// capacity figures or feed a bogus health percentage.
	if b.Design != InvalidNum {
		t.Errorf("Design = %v, want -1 for relative scale", b.Design)
	}
	if b.Full != InvalidNum {
		t.Errorf("Full = %v, want -1 for relative scale", b.Full)
	}
	if b.HealthPercent != InvalidNum {
		t.Errorf("HealthPercent = %v, want -1 (denominator unknown)", b.HealthPercent)
	}
	// The cycle counter is not affected by the capacity capability.
	if b.Cycles != 12 {
		t.Errorf("Cycles = %v, want 12", b.Cycles)
	}
}

func TestApplyWindowsInfoUnknownValues(t *testing.T) {
	b := invalidBattery()
	applyWindowsInfo(&b, batteryInformation{
		DesignedCapacity:    batteryUnknownCapacity,
		FullChargedCapacity: batteryUnknownCapacity,
		CycleCount:          batteryUnknownCapacity,
	})
	if b.Design != InvalidNum || b.Full != InvalidNum {
		t.Errorf("Design/Full = %v/%v, want -1/-1", b.Design, b.Full)
	}
	if b.Cycles != InvalidNum {
		t.Errorf("Cycles = %v, want -1 for 0xFFFFFFFF", b.Cycles)
	}
	// A driver that reports cycle_count = 0 cannot be distinguished from one
	// that does not implement it, so it stays -1 rather than a claimed new cell.
	b2 := invalidBattery()
	applyWindowsInfo(&b2, batteryInformation{CycleCount: 0})
	if b2.Cycles != InvalidNum {
		t.Errorf("Cycles = %v, want -1 for an indistinguishable zero", b2.Cycles)
	}
}

func TestApplyWindowsStatusAbsolutePercentage(t *testing.T) {
	b := invalidBattery()
	b.Full = 50000
	applyWindowsStatus(&b, batteryStatus{
		PowerState: batteryCharging | batteryPowerOnLine,
		Capacity:   25000,
		Voltage:    11400,
		Rate:       15000,
	})

	if b.Percent != 50 {
		t.Errorf("Percent = %v, want 50", b.Percent)
	}
	if b.Current != 25000 {
		t.Errorf("Current = %v, want 25000", b.Current)
	}
	if b.Voltage != 11.4 {
		t.Errorf("Voltage = %v, want 11.4 (mV → V)", b.Voltage)
	}
	if b.ChargeRate != 15000 {
		t.Errorf("ChargeRate = %v, want 15000 (charging → positive)", b.ChargeRate)
	}
	if b.State != "Charging" || b.Charging != TriYes || b.ACPower != TriYes {
		t.Errorf("state = %q charging=%d ac=%d", b.State, b.Charging, b.ACPower)
	}
}

func TestApplyWindowsStatusRelativePercentage(t *testing.T) {
	b := invalidBattery() // Full left at -1 → the relative branch
	applyWindowsStatus(&b, batteryStatus{Capacity: 55})
	if b.Percent != 55 {
		t.Errorf("Percent = %v, want 55 (relative scale is a plain percentage)", b.Percent)
	}

	over := invalidBattery()
	applyWindowsStatus(&over, batteryStatus{Capacity: 9999})
	if over.Percent != InvalidNum || over.Current != InvalidNum {
		t.Errorf("out-of-range relative capacity = %v/%v, want -1/-1", over.Percent, over.Current)
	}
}

func TestApplyWindowsStatusUnknownCapacity(t *testing.T) {
	b := invalidBattery()
	b.Full = 50000
	applyWindowsStatus(&b, batteryStatus{Capacity: batteryUnknownCapacity})
	if b.Percent != InvalidNum || b.Current != InvalidNum {
		t.Errorf("Percent/Current = %v/%v, want -1/-1", b.Percent, b.Current)
	}
}

func TestApplyWindowsStatusUnknownRate(t *testing.T) {
	b := invalidBattery()
	applyWindowsStatus(&b, batteryStatus{Rate: batteryUnknownRate})
	if b.ChargeRate != InvalidNum {
		t.Errorf("ChargeRate = %v, want -1 for BATTERY_UNKNOWN_RATE", b.ChargeRate)
	}
}

func TestApplyWindowsStatusRateSignFollowsDischarge(t *testing.T) {
	b := invalidBattery()
	applyWindowsStatus(&b, batteryStatus{
		PowerState: batteryDischarging,
		Rate:       12000,
	})
	if b.ChargeRate != -12000 {
		t.Errorf("ChargeRate = %v, want -12000 while discharging", b.ChargeRate)
	}
	if b.State != "Discharging" || b.ACPower != TriNo || b.Charging != TriNo {
		t.Errorf("state = %q charging=%d ac=%d", b.State, b.Charging, b.ACPower)
	}
}

func TestApplyWindowsStatusPowerStateFlags(t *testing.T) {
	cases := []struct {
		name         string
		state        uint32
		wantState    string
		wantCharging int
		wantAC       int
	}{
		{"charging", batteryCharging, "Charging", TriYes, TriYes},
		{"discharging", batteryDischarging, "Discharging", TriNo, TriNo},
		{"idle on line", batteryPowerOnLine, "Idle", TriNo, TriYes},
		{"idle off line", 0, "Idle", TriNo, TriUnknown},
	}
	for _, c := range cases {
		b := invalidBattery()
		applyWindowsStatus(&b, batteryStatus{PowerState: c.state})
		if b.State != c.wantState || b.Charging != c.wantCharging || b.ACPower != c.wantAC {
			t.Errorf("%s: state=%q charging=%d ac=%d, want %q/%d/%d",
				c.name, b.State, b.Charging, b.ACPower, c.wantState, c.wantCharging, c.wantAC)
		}
		if b.Present != TriYes {
			t.Errorf("%s: Present = %d, want 1", c.name, b.Present)
		}
	}
}
