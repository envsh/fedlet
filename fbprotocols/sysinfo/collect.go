// collect.go — one sampling round, numeric guards and error aggregation.
//
// This file owns the mechanics shared by every sampler: the -1 rules, the
// deduplicated error collector and the previous-round counter snapshot used to
// derive CPU% and IO/net rates without blocking.
//
// Rule A (function-level): a gopsutil call returning err marks the whole
// section -1 and appends "<section>: <err>".
// Rule B (field-level): guardNum/guardPct replace non-finite, negative or
// non-computable values with -1, leaving the rest of the section intact.
// Rule C (first-round delta): speed/CPU fields stay -1 until a previous
// snapshot exists.
// Rule D (single item): a failing entity inside an array is marked -1 while
// its siblings are still published.
// Rule E (whole section): empty array plus an errors[] entry, never a -1
// sentinel item.
package sysinfo

import (
	"math"
	"sync"
	"time"
)

// maxErrors caps errors[] per round so one broken subsystem cannot flood the
// payload. Individual messages are additionally deduplicated by
// section+message.
const maxErrors = 32

// errCollector accumulates per-round error strings.
type errCollector struct {
	mu   sync.Mutex
	seen map[string]bool
	out  []string
}

func newErrCollector() *errCollector {
	return &errCollector{seen: make(map[string]bool), out: make([]string, 0, 8)}
}

// Add records msg under a section key. Repeats of the same section+message are
// dropped and the total is capped at maxErrors.
func (e *errCollector) Add(section, msg string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	key := section + ":" + msg
	if e.seen[key] {
		return
	}
	if len(e.out) >= maxErrors {
		return
	}
	e.seen[key] = true
	e.out = append(e.out, section+": "+msg)
}

// String returns the accumulated, deduplicated and capped list; nil when empty.
func (e *errCollector) String() []string {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.out) == 0 {
		return nil
	}
	out := make([]string, len(e.out))
	copy(out, e.out)
	return out
}

// guardNum implements rule B for plain values: anything non-finite or negative
// becomes InvalidNum.
func guardNum(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return InvalidNum
	}
	return v
}

// guardPct implements rule B for ratios: a zero/NaN/Inf denominator means the
// percentage is not computable, so it becomes InvalidNum. Otherwise the result
// is clamped to [0,100].
func guardPct(num, den float64) float64 {
	if den <= 0 || math.IsNaN(num) || math.IsNaN(den) ||
		math.IsInf(num, 0) || math.IsInf(den, 0) {
		return InvalidNum
	}
	p := num / den * 100
	if math.IsNaN(p) || math.IsInf(p, 0) {
		return InvalidNum
	}
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

// tri encodes a known/bool pair as the -1/0/1 tri-state used by present,
// charging and ac_power.
func tri(known, val bool) int {
	if !known {
		return TriUnknown
	}
	if val {
		return TriYes
	}
	return TriNo
}

// fillInvalid returns n elements of InvalidNum: used where the cardinality is
// known (core count) but the values themselves are not, so consumers learn how
// many CPUs exist even though the first round has no utilisation reading.
func fillInvalid(n int) []float64 {
	if n <= 0 {
		return []float64{}
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = InvalidNum
	}
	return out
}

// ---- previous-round counter snapshot (rule C) ----

// cpuSample is one CPU's cumulative busy/total jiffies from cpu.Times.
type cpuSample struct {
	name  string
	busy  float64
	total float64
}

// speedSnapshot carries the cumulative counters of the previous round. It is
// nil on the very first round, which is exactly when rates must be -1.
type speedSnapshot struct {
	at       time.Time
	cpuTotal cpuSample
	cpuPer   []cpuSample
	ioRead   map[string]float64
	ioWrite  map[string]float64
	netSent  map[string]float64
	netRecv  map[string]float64
}

// newSpeedSnapshot returns an empty snapshot ready to be filled by the samplers
// that own each counter family.
func newSpeedSnapshot() *speedSnapshot {
	return &speedSnapshot{
		at:      time.Now(),
		ioRead:  make(map[string]float64),
		ioWrite: make(map[string]float64),
		netSent: make(map[string]float64),
		netRecv: make(map[string]float64),
	}
}

// rate returns bytes/second between two cumulative readings taken prevSeconds
// apart, or -1 when the counters went backwards (interface reset) or no
// previous sample exists.
func rate(cur, prev, prevSeconds float64) float64 {
	if prevSeconds <= 0 || cur < 0 || prev < 0 || cur < prev {
		return InvalidNum
	}
	return (cur - prev) / prevSeconds
}

// elapsedSeconds reports how long ago the previous snapshot was taken.
func (s *speedSnapshot) elapsedSeconds(now time.Time) float64 {
	if s == nil || s.at.IsZero() {
		return 0
	}
	d := now.Sub(s.at).Seconds()
	if d <= 0 {
		return 0
	}
	return d
}

// ---- round orchestration ----

// markAllInvalid pre-fills every scalar field of the snapshot with InvalidNum
// so that the default state of an untouched field is "-1 = unavailable" rather
// than "0 = measured zero". Samplers overwrite only what they actually read.
func markAllInvalid(s *Snapshot) {
	s.Host = HostInfo{
		Procs: InvalidNum, UptimeSec: InvalidNum, BootTime: InvalidNum,
	}
	s.CPU = CPUInfo{
		Percent: InvalidNum, CountsLogical: InvalidNum, CountsPhysical: InvalidNum,
		Mhz: InvalidNum, PerCPU: []float64{},
	}
	s.Mem = MemInfo{
		Total: InvalidNum, Available: InvalidNum, Used: InvalidNum, Free: InvalidNum,
		UsedPercent: InvalidNum, SwapTotal: InvalidNum, SwapUsed: InvalidNum,
		SwapUsedPercent: InvalidNum,
	}
	s.Load = LoadInfo{Load1: InvalidNum, Load5: InvalidNum, Load15: InvalidNum}
}

// collectOnce runs every sampler and returns a publishable Snapshot together
// with the counter snapshot to feed the next round (rule C). A failing sampler
// records its error and never aborts the round; the snapshot itself is always
// non-nil so the caller can always publish.
func collectOnce(prev *speedSnapshot) (*Snapshot, *speedSnapshot) {
	errs := newErrCollector()
	next := newSpeedSnapshot()
	snap := &Snapshot{
		Kind:        "sysinfo",
		PublishedAt: time.Now().Unix(),
		Errors:      []string{},
	}
	// Allocation before samplers so consumers never see a null array.
	snap.Disk = []DiskInfo{}
	snap.IO = []IOInfo{}
	snap.Net = []NetInfo{}
	snap.Temp = []TempInfo{}
	snap.Battery = []BatteryInfo{}
	// Safe default: every scalar starts as -1, so a sampler that cannot produce
	// a value leaves the sentinel behind instead of a misleading zero.
	markAllInvalid(snap)

	collectHost(snap, errs)
	collectCPU(snap, errs, prev, next)
	collectMem(snap, errs)
	collectLoad(snap, errs)
	collectDisk(snap, errs)
	collectIO(snap, errs, prev, next)
	collectNet(snap, errs, prev, next)
	collectTemp(snap, errs)
	collectBattery(snap, errs)

	if list := errs.String(); list != nil {
		snap.Errors = list
	}
	return snap, next
}
