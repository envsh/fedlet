// collect_temp.go — thermal sensors.
//
// Temperature is the one section that legitimately does not exist on most
// machines (desktops without exposed diodes, most VMs and containers). That
// case follows rule E: an empty array plus an errors[] entry naming the reason
// — never a single -1 placeholder row.
//
// Sensors reported with a non-positive value are dropped rather than published
// as -1, because a platform that returns 0 means "unknown", not "0 degrees".
package sysinfo

import (
	"github.com/shirou/gopsutil/v4/sensors"
)

// collectTemp fills Snapshot.Temp.
func collectTemp(s *Snapshot, errs *errCollector) {
	temps, err := sensors.SensorsTemperatures()
	if err != nil {
		errs.Add("temp", err.Error())
		return
	}
	for _, t := range temps {
		if t.Temperature <= 0 || t.SensorKey == "" {
			continue
		}
		s.Temp = append(s.Temp, TempInfo{
			Sensor:   t.SensorKey,
			Celsius:  guardNum(t.Temperature),
			High:     guardInvalidIfNegative(t.High),
			Critical: guardInvalidIfNegative(t.Critical),
		})
	}
	if len(s.Temp) == 0 {
		errs.Add("temp", "no temperature sensors reported")
	}
}

// guardInvalidIfNegative keeps a genuine reading but maps a negative one to
// the -1 sentinel: kernel drivers use 0 for "not wired" and negative values to
// mean "threshold not set".
func guardInvalidIfNegative(v float64) float64 {
	if v < 0 {
		return InvalidNum
	}
	return v
}
