// collect_io.go — block device throughput counters.
//
// gopsutil exposes only cumulative totals, so the rate published in
// read_speed/write_speed is computed against the previous round's snapshot. On
// the first round there is nothing to diff and both stay -1 (rule C); a device
// whose counters went backwards (device reset, hot-unplug) also reports -1
// rather than a negative rate.
//
// Keys are sorted so the emitted array has a stable order across rounds and
// between test runs.
package sysinfo

import (
	"sort"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

// collectIO fills Snapshot.IO and records this round's totals into next.
func collectIO(s *Snapshot, errs *errCollector, prev, next *speedSnapshot) {
	counters, err := disk.IOCounters()
	if err != nil {
		errs.Add("io", err.Error())
		return
	}
	if len(counters) == 0 {
		// Rule E: no block devices visible (containers, minimal kernels).
		errs.Add("io", "no block devices reported")
		return
	}

	names := make([]string, 0, len(counters))
	for name := range counters {
		names = append(names, name)
	}
	sort.Strings(names)

	now := time.Now()
	elapsed := prev.elapsedSeconds(now)
	firstRound := prev == nil
	if firstRound {
		errs.Add("io", "first round: no previous io sample")
	}

	for _, name := range names {
		c := counters[name]
		info := IOInfo{
			Name:       c.Name,
			ReadCount:  guardNum(float64(c.ReadCount)),
			WriteCount: guardNum(float64(c.WriteCount)),
			ReadBytes:  guardNum(float64(c.ReadBytes)),
			WriteBytes: guardNum(float64(c.WriteBytes)),
			ReadSpeed:  InvalidNum,
			WriteSpeed: InvalidNum,
		}
		if info.Name == "" {
			info.Name = name
		}
		next.ioRead[name] = float64(c.ReadBytes)
		next.ioWrite[name] = float64(c.WriteBytes)

		if !firstRound {
			if p, ok := prev.ioRead[name]; ok {
				info.ReadSpeed = guardNum(rate(float64(c.ReadBytes), p, elapsed))
			}
			if p, ok := prev.ioWrite[name]; ok {
				info.WriteSpeed = guardNum(rate(float64(c.WriteBytes), p, elapsed))
			}
		}
		s.IO = append(s.IO, info)
	}
}
