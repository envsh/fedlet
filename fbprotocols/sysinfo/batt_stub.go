//go:build !linux && !android && !darwin && !windows

// batt_stub.go — battery reader for platforms without an implementation.
//
// FreeBSD and friends still get every other metric; only the battery section
// is missing, and it surfaces as an errors[] entry plus an empty array
// (rule E) rather than as a fabricated -1 row.
package sysinfo

import (
	"fmt"
	"runtime"
)

// readBatteries reports that this platform has no battery reader.
func readBatteries(errs *errCollector) ([]BatteryInfo, error) {
	return nil, fmt.Errorf("battery reading not implemented for %s", runtime.GOOS)
}
