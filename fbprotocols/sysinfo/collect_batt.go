// collect_batt.go — battery sampler plumbing shared by every platform.
//
// The per-platform readers live in batt_linux.go, batt_darwin.go,
// batt_windows.go and batt_stub.go; each of them returns the raw list and the
// first error. This file owns the shared shape of that list: indexing, the
// "no battery" rule-E case and the sentinel defaults every field starts from.
//
// Battery is the one section that is never allowed to be empty, so every path
// that has no real battery appends exactly one sentinel row (present:0 when
// absent, -1 when unreadable) on top of the errors[] entry, which is unchanged.
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
		s.Battery = append(s.Battery, absentBattery(TriUnknown))
		return
	}
	if len(bats) == 0 {
		// Rule E: a machine that genuinely has no battery (desktop, VM, board).
		errs.Add("battery", "no battery present")
		s.Battery = append(s.Battery, absentBattery(TriNo))
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

// absentBattery is the row published when there is no battery to report. Its
// scalars are all the -1 sentinel; present carries the reason: TriNo when the
// machine genuinely has no battery, TriUnknown when the reader failed and
// presence could not be determined. Index is filled by collectBattery.
func absentBattery(present int) BatteryInfo {
	b := invalidBattery()
	b.Present = present
	return b
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
