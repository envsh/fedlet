// collect_batt.go — battery sampler plumbing shared by every platform.
//
// The per-platform readers live in batt_linux.go, batt_darwin.go,
// batt_windows.go and batt_stub.go; each of them returns the raw list and the
// first error. This file owns the shared shape of that list: indexing, the
// "no battery" rule-E case and the sentinel defaults every field starts from.
//
// There is deliberately no -1 placeholder row: a desktop or a VM without a
// battery publishes Battery = [] plus an errors[] entry saying so.
package sysinfo

// readBatteriesFn is an indirection point so the battery section can be tested
// on a host without a battery (mirrors the fetchHotlistFn pattern in toutiao).
var readBatteriesFn = readBatteries

// collectBattery fills Snapshot.Battery.
func collectBattery(s *Snapshot, errs *errCollector) {
	bats, err := readBatteriesFn(errs)
	if err != nil {
		// Rule E: the platform could not be queried at all.
		errs.Add("battery", err.Error())
		return
	}
	if len(bats) == 0 {
		// Rule E: a machine that genuinely has no battery (desktop, VM, board).
		errs.Add("battery", "no battery present")
		return
	}
	for i := range bats {
		bats[i].Index = i
		s.Battery = append(s.Battery, bats[i])
	}
}

// invalidBattery returns a BatteryInfo whose scalar fields are all the -1
// sentinel; platform readers overwrite only what they actually measured.
func invalidBattery() BatteryInfo {
	return BatteryInfo{
		Present:       TriYes,
		Percent:       InvalidNum,
		Charging:      TriUnknown,
		HealthPercent: InvalidNum,
		Cycles:        InvalidNum,
		State:         "",
		ACPower:       TriUnknown,
		Current:       InvalidNum,
		Full:          InvalidNum,
		Design:        InvalidNum,
		ChargeRate:    InvalidNum,
		Voltage:       InvalidNum,
		Temperature:   InvalidNum,
	}
}

// healthPercent derives health_percent from a full-charged/design-capacity
// pair. A missing design capacity leaves the -1 sentinel, because a percentage
// computed against zero is meaningless rather than merely unknown.
func healthPercent(full, design float64) float64 {
	if design <= 0 || full < 0 {
		return InvalidNum
	}
	return guardPct(full, design)
}
