// collect_sys.go — host, cpu, mem and load samplers.
//
// All four are thin adapters over gopsutil; this file is where rule A (whole
// section -1 on error) and rule B (per-field guard) meet. CPU utilisation is
// derived from two consecutive cpu.Times readings held in speedSnapshot rather
// than cpu.Percent(0, ...), because the delta must be diffed against our own
// previous round (rule C) and must never block the loop.
package sysinfo

import (
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

// collectHost fills Snapshot.Host. On error the -1 defaults from
// markAllInvalid stay in place (rule A).
func collectHost(s *Snapshot, errs *errCollector) {
	h, err := host.Info()
	if err != nil {
		errs.Add("host", err.Error())
		return
	}
	s.Host.Hostname = h.Hostname
	s.Host.OS = h.OS
	s.Host.Platform = h.Platform
	s.Host.PlatformVersion = h.PlatformVersion
	s.Host.Kernel = h.KernelVersion
	s.Host.Arch = h.KernelArch
	s.Host.HostID = h.HostID
	switch {
	case h.VirtualizationSystem == "":
	case h.VirtualizationRole == "":
		s.Host.Virtualization = h.VirtualizationSystem
	default:
		s.Host.Virtualization = h.VirtualizationSystem + "/" + h.VirtualizationRole
	}
	s.Host.Procs = guardNum(float64(h.Procs))
	s.Host.UptimeSec = guardNum(float64(h.Uptime))
	s.Host.BootTime = guardNum(float64(h.BootTime))
}

// jiffies converts a cpu.TimesStat into cumulative busy/total counters.
//
// guest is deliberately not added: on Linux it is already accounted for inside
// user, and iowait is treated as idle so utilisation reflects time the CPU was
// actually running something.
func jiffies(t cpu.TimesStat) cpuSample {
	total := t.User + t.System + t.Nice + t.Idle + t.Iowait + t.Irq + t.Softirq + t.Steal
	busy := total - t.Idle - t.Iowait
	return cpuSample{name: t.CPU, busy: busy, total: total}
}

// pctFromDiff converts two cumulative samples into a utilisation percentage.
func pctFromDiff(cur, prev cpuSample) float64 {
	dt := cur.total - prev.total
	if dt <= 0 {
		return InvalidNum
	}
	return guardPct(cur.busy-prev.busy, dt)
}

// collectCPU fills Snapshot.CPU and records this round's counters into next.
// Percent/PerCPU are -1 on the first round (rule C).
func collectCPU(s *Snapshot, errs *errCollector, prev, next *speedSnapshot) {
	if n, err := cpu.Counts(true); err != nil {
		errs.Add("cpu", err.Error())
	} else {
		s.CPU.CountsLogical = float64(n)
	}
	if n, err := cpu.Counts(false); err != nil {
		errs.Add("cpu", err.Error())
	} else {
		s.CPU.CountsPhysical = float64(n)
	}
	if infos, err := cpu.Info(); err != nil {
		errs.Add("cpu", err.Error())
	} else if len(infos) > 0 {
		s.CPU.Model = infos[0].ModelName
		s.CPU.Mhz = guardNum(infos[0].Mhz)
	}

	totals, err := cpu.Times(false)
	switch {
	case err != nil:
		errs.Add("cpu", err.Error())
	case len(totals) == 0:
		// gopsutil returns an empty slice with a nil error when /proc/stat is
		// unreadable, so this branch still has to count as a failure.
		errs.Add("cpu", "no aggregate cpu line in /proc/stat")
	default:
		next.cpuTotal = jiffies(totals[0])
	}

	per, err := cpu.Times(true)
	if err != nil {
		errs.Add("cpu", err.Error())
		return
	}
	if len(per) == 0 {
		errs.Add("cpu", "no per-cpu lines in /proc/stat")
		s.CPU.PerCPU = []float64{}
		return
	}
	next.cpuPer = make([]cpuSample, len(per))
	for i, t := range per {
		next.cpuPer[i] = jiffies(t)
	}

	if prev == nil || len(prev.cpuPer) == 0 || prev.cpuTotal.total <= 0 {
		// Rule C: no previous sample, so there is nothing to diff against.
		errs.Add("cpu", "first round: no previous cpu sample")
		s.CPU.PerCPU = fillInvalid(len(per))
		return
	}
	s.CPU.Percent = pctFromDiff(next.cpuTotal, prev.cpuTotal)

	out := make([]float64, len(per))
	for i := range per {
		if i >= len(prev.cpuPer) {
			out[i] = InvalidNum
			continue
		}
		out[i] = pctFromDiff(next.cpuPer[i], prev.cpuPer[i])
	}
	s.CPU.PerCPU = out
}

// collectMem fills memory and swap. Swap with a zero total is a valid "no swap
// configured" state rather than an unavailable value, so its percentage is 0
// instead of -1 and no error is recorded.
func collectMem(s *Snapshot, errs *errCollector) {
	v, err := mem.VirtualMemory()
	if err != nil {
		errs.Add("mem", err.Error())
	} else {
		s.Mem.Total = guardNum(float64(v.Total))
		s.Mem.Available = guardNum(float64(v.Available))
		s.Mem.Used = guardNum(float64(v.Used))
		s.Mem.Free = guardNum(float64(v.Free))
		s.Mem.UsedPercent = guardPct(float64(v.Used), float64(v.Total))
	}

	sw, err := mem.SwapMemory()
	if err != nil {
		errs.Add("mem", err.Error())
		return
	}
	s.Mem.SwapTotal = guardNum(float64(sw.Total))
	s.Mem.SwapUsed = guardNum(float64(sw.Used))
	if sw.Total == 0 {
		s.Mem.SwapUsedPercent = 0
		return
	}
	s.Mem.SwapUsedPercent = guardPct(float64(sw.Used), float64(sw.Total))
}

// collectLoad fills the load averages. Negative readings are rejected because
// a load average can never fall below zero.
func collectLoad(s *Snapshot, errs *errCollector) {
	a, err := load.Avg()
	if err != nil {
		errs.Add("load", err.Error())
		return
	}
	s.Load.Load1 = guardNum(a.Load1)
	s.Load.Load5 = guardNum(a.Load5)
	s.Load.Load15 = guardNum(a.Load15)
}
