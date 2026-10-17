package chargectl

import (
	"strconv"
	"strings"
)

// macBattery is the subset of AppleSmartBattery state chargectl needs. The
// parsers live in this build-tag-free file so they unit-test on any platform.
type macBattery struct {
	installed     bool
	capacity      int  // percent, or -1 when unknown
	externalPower bool // charger attached
	charging      bool
	fullyCharged  bool
}

// parseIoregBattery extracts the battery fields from
// `ioreg -rn AppleSmartBattery`. On Apple silicon CurrentCapacity is already a
// percentage and MaxCapacity is 100; on Intel both are mAh, so the percentage
// is derived whenever MaxCapacity exceeds 100.
func parseIoregBattery(out string) macBattery {
	var b macBattery
	var cur, max int
	var haveCur bool
	for _, ln := range strings.Split(out, "\n") {
		k, v, ok := ioregPair(ln)
		if !ok {
			continue
		}
		switch k {
		case "BatteryInstalled":
			b.installed = v == "Yes"
		case "ExternalConnected":
			b.externalPower = v == "Yes"
		case "IsCharging":
			b.charging = v == "Yes"
		case "FullyCharged":
			b.fullyCharged = v == "Yes"
		case "CurrentCapacity":
			if n, err := strconv.Atoi(v); err == nil {
				cur, haveCur = n, true
			}
		case "MaxCapacity":
			max, _ = strconv.Atoi(v)
		}
	}
	b.capacity = -1
	if haveCur {
		if max > 100 {
			b.capacity = (cur*100 + max/2) / max
		} else {
			b.capacity = cur
		}
	}
	return b
}

// ioregPair parses one `"Key" = Value` line of ioreg output.
func ioregPair(ln string) (key, val string, ok bool) {
	l := strings.TrimSpace(ln)
	if !strings.HasPrefix(l, "\"") {
		return "", "", false
	}
	end := strings.IndexByte(l[1:], '"')
	if end < 0 {
		return "", "", false
	}
	rest := strings.TrimSpace(l[end+2:])
	if !strings.HasPrefix(rest, "=") {
		return "", "", false
	}
	return l[1 : end+1], strings.TrimSpace(rest[1:]), true
}

// parsePmsetBatt parses `pmset -g batt`, the fallback when ioreg yields nothing
// (older macOS, unusual hardware). Example lines:
//
//	Now drawing from 'AC Power'
//	 -InternalBattery-0 (id=1)	78%; charged; 0:00 remaining present: true
func parsePmsetBatt(out string) macBattery {
	b := macBattery{capacity: -1, installed: true}
	for _, ln := range strings.Split(out, "\n") {
		l := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(l, "Now drawing from"):
			b.externalPower = strings.Contains(l, "AC Power")
		case strings.Contains(l, "%"):
			if i := strings.IndexByte(l, '%'); i > 0 {
				j := i - 1
				for j >= 0 && l[j] >= '0' && l[j] <= '9' {
					j--
				}
				if n, err := strconv.Atoi(l[j+1 : i]); err == nil {
					b.capacity = n
				}
			}
			b.charging = strings.Contains(l, "charging")
			b.fullyCharged = strings.Contains(l, "charged") && !strings.Contains(l, "discharging")
		}
	}
	return b
}
