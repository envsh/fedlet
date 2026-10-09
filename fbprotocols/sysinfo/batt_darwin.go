//go:build darwin

// batt_darwin.go — battery reading through the IOKit registry.
//
// `ioreg -n AppleSmartBattery -r -a` dumps the built-in battery as an XML
// plist, which is the only interface on macOS that exposes both the design
// capacity and the charge cycle count in one read (IOKit's
// IOPSCopyPowerSourcesInfo, by contrast, reports neither).
package sysinfo

import (
	"fmt"
	"os/exec"
)

// readBatteries runs ioreg and decodes its plist output. Parsing and unit
// conversion live in batt_ioreg.go (untagged) so they can be unit-tested on
// any host, not just macOS.
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
