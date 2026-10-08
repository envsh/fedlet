// collect_net.go — per-interface counters and rates.
//
// Interface flags and MAC addresses come from net.Interfaces while the byte
// counters come from net.IOCounters(true); an interface present in one list but
// absent from the other is still published, with the missing half left at -1
// (rule D). Rates follow rule C: -1 on the first round and whenever a counter
// regresses (counter reset on interface re-creation).
//
// Keys are sorted so the emitted array has a stable order across rounds.
package sysinfo

import (
	"sort"
	"time"

	"github.com/shirou/gopsutil/v4/net"
)

// collectNet fills Snapshot.Net and records this round's totals into next.
func collectNet(s *Snapshot, errs *errCollector, prev, next *speedSnapshot) {
	counters, err := net.IOCounters(true)
	if err != nil {
		errs.Add("net", err.Error())
		return
	}
	if len(counters) == 0 {
		// Rule E: nothing enumerable on this platform.
		errs.Add("net", "no interfaces reported")
		return
	}

	flags := make(map[string][]string, 4)
	macs := make(map[string]string, 4)
	if ifaces, ierr := net.Interfaces(); ierr != nil {
		errs.Add("net", ierr.Error())
	} else {
		for _, in := range ifaces {
			if len(in.Flags) > 0 {
				flags[in.Name] = in.Flags
			}
			if in.HardwareAddr != "" {
				macs[in.Name] = in.HardwareAddr
			}
		}
	}

	names := make([]string, 0, len(counters))
	for _, c := range counters {
		names = append(names, c.Name)
	}
	sort.Strings(names)

	now := time.Now()
	elapsed := prev.elapsedSeconds(now)
	firstRound := prev == nil
	if firstRound {
		errs.Add("net", "first round: no previous net sample")
	}

	for _, name := range names {
		c, ok := counterByName(counters, name)
		if !ok {
			continue
		}
		info := NetInfo{
			Name:        name,
			MAC:         macs[name],
			Flags:       flagList(flags[name]),
			BytesSent:   guardNum(float64(c.BytesSent)),
			BytesRecv:   guardNum(float64(c.BytesRecv)),
			PacketsSent: guardNum(float64(c.PacketsSent)),
			PacketsRecv: guardNum(float64(c.PacketsRecv)),
			ErrIn:       guardNum(float64(c.Errin)),
			ErrOut:      guardNum(float64(c.Errout)),
			DropIn:      guardNum(float64(c.Dropin)),
			DropOut:     guardNum(float64(c.Dropout)),
			SendSpeed:   InvalidNum,
			RecvSpeed:   InvalidNum,
		}
		next.netSent[name] = float64(c.BytesSent)
		next.netRecv[name] = float64(c.BytesRecv)

		if !firstRound {
			if p, ok := prev.netSent[name]; ok {
				info.SendSpeed = guardNum(rate(float64(c.BytesSent), p, elapsed))
			}
			if p, ok := prev.netRecv[name]; ok {
				info.RecvSpeed = guardNum(rate(float64(c.BytesRecv), p, elapsed))
			}
		}
		s.Net = append(s.Net, info)
	}
}

// counterByName looks up an interface counter; the two gopsutil calls must not
// disagree about ordering, only about membership.
func counterByName(list []net.IOCountersStat, name string) (net.IOCountersStat, bool) {
	for _, c := range list {
		if c.Name == name {
			return c, true
		}
	}
	return net.IOCountersStat{}, false
}

// flagList turns a nil flag slice into an empty one so the JSON key is always
// an array rather than null.
func flagList(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
