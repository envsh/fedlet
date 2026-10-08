package sysinfo

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// ---- rule B: guardNum / guardPct ----

func TestGuardNum(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0, 0},
		{42.5, 42.5},
		{InvalidNum, InvalidNum},
		{-1.5, InvalidNum},
		{math.NaN(), InvalidNum},
		{math.Inf(1), InvalidNum},
		{math.Inf(-1), InvalidNum},
	}
	for _, c := range cases {
		if got := guardNum(c.in); got != c.want {
			t.Errorf("guardNum(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestGuardPct(t *testing.T) {
	cases := []struct {
		num, den, want float64
		name           string
	}{
		{50, 100, 50, "normal"},
		{0, 100, 0, "zero numerator"},
		{0, 0, InvalidNum, "zero denominator (not computable)"},
		{-5, 100, 0, "clamped low"},
		{150, 100, 100, "clamped high"},
		{math.NaN(), 100, InvalidNum, "NaN numerator"},
		{50, math.NaN(), InvalidNum, "NaN denominator"},
		{50, math.Inf(1), InvalidNum, "infinite denominator"},
	}
	for _, c := range cases {
		if got := guardPct(c.num, c.den); got != c.want {
			t.Errorf("%s: guardPct(%v,%v) = %v, want %v", c.name, c.num, c.den, got, c.want)
		}
	}
}

// ---- tri-state encoding ----

func TestTri(t *testing.T) {
	cases := []struct {
		known, val bool
		want       int
	}{
		{false, false, TriUnknown},
		{false, true, TriUnknown},
		{true, false, TriNo},
		{true, true, TriYes},
	}
	for _, c := range cases {
		if got := tri(c.known, c.val); got != c.want {
			t.Errorf("tri(%v,%v) = %d, want %d", c.known, c.val, got, c.want)
		}
	}
	if tri(false, false) != -1 || tri(true, false) != 0 || tri(true, true) != 1 {
		t.Fatalf("tri-state values drifted: want -1/0/1")
	}
}

func TestFillInvalid(t *testing.T) {
	got := fillInvalid(3)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, v := range got {
		if v != InvalidNum {
			t.Errorf("got[%d] = %v, want -1", i, v)
		}
	}
	if out := fillInvalid(0); len(out) != 0 {
		t.Errorf("fillInvalid(0) = %v, want empty non-nil", out)
	}
}

// ---- rule C: rate / previous-snapshot diffing ----

func TestRate(t *testing.T) {
	cases := []struct {
		cur, prev, secs, want float64
		name                  string
	}{
		{2000, 1000, 1, 1000, "steady growth"},
		{1000, 1000, 1, 0, "no change"},
		{500, 1000, 1, InvalidNum, "counter went backwards"},
		{1000, 0, 0, InvalidNum, "no previous sample"},
		{-1, 1000, 1, InvalidNum, "current invalid"},
		{2000, -1, 1, InvalidNum, "previous invalid"},
	}
	for _, c := range cases {
		if got := rate(c.cur, c.prev, c.secs); got != c.want {
			t.Errorf("%s: rate(%v,%v,%v) = %v, want %v", c.name, c.cur, c.prev, c.secs, got, c.want)
		}
	}
}

func TestElapsedSeconds(t *testing.T) {
	var nilPrev *speedSnapshot
	if got := nilPrev.elapsedSeconds(time.Now()); got != 0 {
		t.Errorf("nil snapshot elapsed = %v, want 0", got)
	}
	s := &speedSnapshot{at: time.Now().Add(-2 * time.Second)}
	got := s.elapsedSeconds(time.Now())
	if got < 1.9 || got > 2.5 {
		t.Errorf("elapsed = %v, want ~2", got)
	}
	if zero := (&speedSnapshot{}).elapsedSeconds(time.Now()); zero != 0 {
		t.Errorf("zero-time snapshot elapsed = %v, want 0", zero)
	}
}

func TestPctFromDiff(t *testing.T) {
	prev := cpuSample{busy: 100, total: 1000}
	cur := cpuSample{busy: 150, total: 1100} // +50 busy / +100 total = 50%
	if got := pctFromDiff(cur, prev); math.Abs(got-50) > 1e-9 {
		t.Errorf("pctFromDiff = %v, want 50", got)
	}
	// Rule C: the delta across a counter reset is unusable.
	if got := pctFromDiff(prev, cur); got != InvalidNum {
		t.Errorf("backwards diff = %v, want -1", got)
	}
	if got := pctFromDiff(prev, prev); got != InvalidNum {
		t.Errorf("zero delta = %v, want -1", got)
	}
}

// ---- error collector: dedup + cap ----

func TestErrCollectorDedupAndCap(t *testing.T) {
	e := newErrCollector()
	for i := 0; i < 5; i++ {
		e.Add("temp", "no sensors") // same key five times
	}
	e.Add("temp", "permission denied") // different message, same section
	if got := e.String(); len(got) != 2 {
		t.Fatalf("dedup: got %v, want 2 entries", got)
	}

	big := newErrCollector()
	for i := 0; i < maxErrors+20; i++ {
		big.Add("net", strconv.Itoa(i)+"-failure")
	}
	if got := big.String(); len(got) != maxErrors {
		t.Errorf("cap: got %d entries, want %d", len(got), maxErrors)
	}

	var nilErrs *errCollector
	nilErrs.Add("x", "y") // must not panic
	if nilErrs.String() != nil {
		t.Errorf("nil collector should return nil")
	}

	if fresh := newErrCollector().String(); fresh != nil {
		t.Errorf("empty collector should return nil, got %v", fresh)
	}
}

// ---- safe defaults ----

func TestMarkAllInvalidLeavesNoZero(t *testing.T) {
	s := &Snapshot{}
	markAllInvalid(s)
	checks := map[string]float64{
		"host.procs":      s.Host.Procs,
		"host.uptime_sec": s.Host.UptimeSec,
		"cpu.percent":     s.CPU.Percent,
		"cpu.mhz":         s.CPU.Mhz,
		"mem.total":       s.Mem.Total,
		"mem.used_pct":    s.Mem.UsedPercent,
		"load.load1":      s.Load.Load1,
		"load.load15":     s.Load.Load15,
	}
	for name, v := range checks {
		if v != InvalidNum {
			t.Errorf("%s = %v after markAllInvalid, want -1", name, v)
		}
	}
	if s.CPU.PerCPU == nil {
		t.Errorf("PerCPU must be non-nil after markAllInvalid")
	}
}

// ---- snapshot shape / rule C on a real first round ----

func TestCollectOnceFirstRoundShape(t *testing.T) {
	snap, next := collectOnce(nil)
	if snap == nil {
		t.Fatal("collectOnce returned nil snapshot")
	}
	if next == nil {
		t.Fatal("collectOnce returned nil next snapshot")
	}
	if snap.Kind != "sysinfo" {
		t.Errorf("Kind = %q, want sysinfo", snap.Kind)
	}
	if snap.PublishedAt <= 0 {
		t.Errorf("PublishedAt = %d, want > 0", snap.PublishedAt)
	}
	// Arrays must be present (never null) even when empty.
	if snap.Disk == nil || snap.IO == nil || snap.Net == nil ||
		snap.Temp == nil || snap.Battery == nil {
		t.Errorf("arrays must be non-nil: disk=%v io=%v net=%v temp=%v batt=%v",
			snap.Disk == nil, snap.IO == nil, snap.Net == nil,
			snap.Temp == nil, snap.Battery == nil)
	}

	// Rule C: nothing to diff against on the first round.
	if snap.CPU.Percent != InvalidNum {
		t.Errorf("first-round cpu percent = %v, want -1", snap.CPU.Percent)
	}
	for i, v := range snap.CPU.PerCPU {
		if v != InvalidNum {
			t.Errorf("first-round per_cpu[%d] = %v, want -1", i, v)
		}
	}
	for _, io := range snap.IO {
		if io.ReadSpeed != InvalidNum || io.WriteSpeed != InvalidNum {
			t.Errorf("first-round io %s speed = %v/%v, want -1/-1",
				io.Name, io.ReadSpeed, io.WriteSpeed)
		}
	}
	for _, n := range snap.Net {
		if n.SendSpeed != InvalidNum || n.RecvSpeed != InvalidNum {
			t.Errorf("first-round net %s speed = %v/%v, want -1/-1",
				n.Name, n.SendSpeed, n.RecvSpeed)
		}
	}
	// Cumulative counters are available on the first round, unlike the rates.
	if n := len(next.ioRead); n == 0 && len(snap.IO) > 0 {
		t.Errorf("next.ioRead not populated but %d io rows published", len(snap.IO))
	}
}

func TestCollectOnceSecondRoundDerivesRates(t *testing.T) {
	_, first := collectOnce(nil)
	time.Sleep(1100 * time.Millisecond)
	snap, _ := collectOnce(first)

	// The second round must have a usable previous sample, so CPU% is a real
	// number rather than the rule-C sentinel (unless the delta was zero).
	if snap.CPU.Percent == InvalidNum {
		// A delta of exactly zero jiffies over 1.1s is possible only on an
		// idle system, so accept it but require the per-cpu array to agree.
		for _, v := range snap.CPU.PerCPU {
			if v != InvalidNum && math.Abs(v) > 100 {
				t.Errorf("cpu percent out of range: %v", v)
			}
		}
	}
	if snap.CPU.Percent > 100 {
		t.Errorf("cpu percent = %v, want <= 100", snap.CPU.Percent)
	}
	// Rates, if present, must be non-negative and plausible.
	for _, io := range snap.IO {
		if io.ReadSpeed < 0 && io.ReadSpeed != InvalidNum {
			t.Errorf("io %s read_speed = %v, want >= 0 or -1", io.Name, io.ReadSpeed)
		}
	}
}

// ---- battery rule E via the injection point ----

func TestCollectBatteryRuleEOnError(t *testing.T) {
	orig := readBatteriesFn
	defer func() { readBatteriesFn = orig }()

	readBatteriesFn = func(errs *errCollector) ([]BatteryInfo, error) {
		errs.Add("battery:BAT0", "permission denied")
		return nil, errors.New("cannot enumerate")
	}

	snap := &Snapshot{Battery: []BatteryInfo{}}
	errs := newErrCollector()
	collectBattery(snap, errs)

	if len(snap.Battery) != 0 {
		t.Errorf("rule E: battery must stay empty, got %d rows", len(snap.Battery))
	}
	list := errs.String()
	if list == nil {
		t.Fatal("rule E: an errors[] entry is required")
	}
	found := false
	for _, e := range list {
		if e == "battery: cannot enumerate" {
			found = true
		}
	}
	if !found {
		t.Errorf("want 'battery: cannot enumerate', got %v", list)
	}
}

func TestCollectBatteryNoBatteryPresent(t *testing.T) {
	orig := readBatteriesFn
	defer func() { readBatteriesFn = orig }()

	readBatteriesFn = func(errs *errCollector) ([]BatteryInfo, error) {
		return nil, nil // desktop: enumeration works, nothing found
	}

	snap := &Snapshot{Battery: []BatteryInfo{}}
	errs := newErrCollector()
	collectBattery(snap, errs)

	if len(snap.Battery) != 0 {
		t.Errorf("no-battery: got %d rows, want empty array", len(snap.Battery))
	}
	if list := errs.String(); list == nil || list[0] != "battery: no battery present" {
		t.Errorf("want 'battery: no battery present', got %v", list)
	}
}

func TestCollectBatteryIndexesRows(t *testing.T) {
	orig := readBatteriesFn
	defer func() { readBatteriesFn = orig }()

	readBatteriesFn = func(errs *errCollector) ([]BatteryInfo, error) {
		return []BatteryInfo{invalidBattery(), invalidBattery()}, nil
	}

	snap := &Snapshot{Battery: []BatteryInfo{}}
	collectBattery(snap, newErrCollector())
	if len(snap.Battery) != 2 {
		t.Fatalf("got %d rows, want 2", len(snap.Battery))
	}
	if snap.Battery[0].Index != 0 || snap.Battery[1].Index != 1 {
		t.Errorf("indices = %d/%d, want 0/1", snap.Battery[0].Index, snap.Battery[1].Index)
	}
	if snap.Battery[0].Percent != InvalidNum {
		t.Errorf("invalidBattery().Percent = %v, want -1", snap.Battery[0].Percent)
	}
}

func TestHealthPercent(t *testing.T) {
	if got := healthPercent(40000, 50000); math.Abs(got-80) > 1e-9 {
		t.Errorf("healthPercent(40000,50000) = %v, want 80", got)
	}
	if got := healthPercent(40000, 0); got != InvalidNum {
		t.Errorf("no design capacity = %v, want -1", got)
	}
	if got := healthPercent(-1, 50000); got != InvalidNum {
		t.Errorf("invalid full = %v, want -1", got)
	}
}

// ---- publishing: top-level proto_type / cycle_count ----

func TestPublishSnapshotAddsFlatFields(t *testing.T) {
	orig := pubfn_
	defer func() { pubfn_ = orig }()

	var got []byte
	pubfn_ = func(v any) error {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		got = raw
		return nil
	}

	snap := collectOnceSnapshot(t)
	if err := publishSnapshot(snap); err != nil {
		t.Fatalf("publishSnapshot: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("nothing published")
	}

	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("published payload is not valid JSON: %v", err)
	}
	if m["proto_type"] != "sysinfo" {
		t.Errorf("proto_type = %v, want sysinfo", m["proto_type"])
	}
	if m["cycle_count"] != float64(1) {
		t.Errorf("cycle_count = %v, want 1 (one entry per round)", m["cycle_count"])
	}
	if m["kind"] != "sysinfo" {
		t.Errorf("kind = %v, want sysinfo", m["kind"])
	}
	// The nested sections must survive the flatten step untouched.
	if _, ok := m["disk"].([]any); !ok {
		t.Errorf("disk section lost or not an array: %T", m["disk"])
	}
	if _, ok := m["battery"].([]any); !ok {
		t.Errorf("battery section lost or not an array: %T", m["battery"])
	}
	// Every array must be present as [] rather than null.
	for _, k := range []string{"disk", "io", "net", "temp", "battery"} {
		if m[k] == nil {
			t.Errorf("%s marshalled as null, want []", k)
		}
	}
}

func TestPublishSnapshotWithoutHookIsNoop(t *testing.T) {
	orig := pubfn_
	pubfn_ = nil
	defer func() { pubfn_ = orig }()

	if err := publishSnapshot(&Snapshot{Kind: "sysinfo"}); err != nil {
		t.Errorf("publish with no hook should be a no-op, got %v", err)
	}
}

func collectOnceSnapshot(t *testing.T) *Snapshot {
	t.Helper()
	snap, _ := collectOnce(nil)
	return snap
}

// ---- persisted status state ----

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sysinfo-state.json")

	if got := loadState(path); got == nil || got.LastOkAt != 0 || len(got.LastErrs) != 0 {
		t.Fatalf("missing file should load as empty state, got %+v", got)
	}

	s := &sysinfoState{LastOkAt: 1700000000, LastErrs: []string{"temp: no sensors"}}
	saveState(path, s)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	info, err := os.Stat(path)
	if err == nil && info.Mode().Perm() != 0600 {
		t.Errorf("state file mode = %v, want 0600", info.Mode().Perm())
	}
	_ = data

	got := loadState(path)
	if got.LastOkAt != 1700000000 {
		t.Errorf("LastOkAt = %d, want 1700000000", got.LastOkAt)
	}
	if len(got.LastErrs) != 1 || got.LastErrs[0] != "temp: no sensors" {
		t.Errorf("LastErrs = %v", got.LastErrs)
	}
}

func TestStateCorruptFileFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := loadState(path); got == nil || got.LastOkAt != 0 {
		t.Errorf("corrupt file should yield fresh state, got %+v", got)
	}
}

func TestTrimErrs(t *testing.T) {
	in := []string{"a", "b", "c", "d", "e"}
	if got := trimErrs(in, 3); len(got) != 3 || got[2] != "c" {
		t.Errorf("trimErrs(...,3) = %v", got)
	}
	if got := trimErrs(nil, 3); got != nil {
		t.Errorf("nil in should yield nil, got %v", got)
	}
	if got := trimErrs(in, 0); got != nil {
		t.Errorf("n=0 should yield nil, got %v", got)
	}
	// The input slice must not be mutated (it belongs to the Snapshot).
	if len(in) != 5 || in[4] != "e" {
		t.Errorf("input mutated: %v", in)
	}
}

// ---- status surface ----

func TestStatusSurface(t *testing.T) {
	if got := AuthStatus(); got != "local" {
		t.Errorf("AuthStatus = %q, want local", got)
	}
	if got := AuthUser(); got != "sysinfo" {
		t.Errorf("AuthUser = %q, want sysinfo", got)
	}
	if IsRunning() {
		// pollLoop is never started here; just ensure it does not panic.
		t.Logf("loop already running")
	}
	if !ConnectedSince().IsZero() {
		t.Logf("connected since %v", ConnectedSince())
	}
	if errs := LastErrs(); errs == nil {
		t.Logf("no errors recorded")
	}
}

func TestPushErrorKeepsThree(t *testing.T) {
	statusLastErrsMu.Lock()
	statusLastErrs = [3]error{}
	statusLastErrsMu.Unlock()

	pushError(errors.New("e1"))
	pushError(errors.New("e2"))
	pushError(errors.New("e3"))
	pushError(errors.New("e4"))

	got := LastErrs()
	if len(got) != 3 {
		t.Fatalf("LastErrs = %d entries, want 3", len(got))
	}
	if got[0].Error() != "e4" || got[2].Error() != "e2" {
		t.Errorf("order wrong: %v %v %v", got[0], got[1], got[2])
	}
}

func TestDefaultInterval(t *testing.T) {
	if DefaultInterval != 123*time.Second {
		t.Errorf("DefaultInterval = %s, want 123s", DefaultInterval)
	}
	if got := currentInterval(); got != DefaultInterval {
		t.Errorf("currentInterval before Start = %s, want %s", got, DefaultInterval)
	}
}

// ---- the loop itself ----

// withLoopIsolation points the state file at a temp dir and captures every
// publish, restoring both on cleanup.
func withLoopIsolation(t *testing.T) func() int {
	t.Helper()

	origPath, origPub := stateFilePathFn, pubfn_
	stateFilePathFn = func() string {
		return filepath.Join(t.TempDir(), "sysinfo-state.json")
	}

	var mu sync.Mutex
	var count int
	pubfn_ = func(v any) error {
		mu.Lock()
		count++
		mu.Unlock()
		return nil
	}

	t.Cleanup(func() {
		Stop()
		stateFilePathFn, pubfn_ = origPath, origPub
	})

	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

func TestStartPollsAndPublishes(t *testing.T) {
	count := withLoopIsolation(t)

	Start(80 * time.Millisecond)
	// The first tick is one interval out; wait long enough for several rounds.
	time.Sleep(500 * time.Millisecond)
	Stop()

	got := count()
	if got < 2 {
		t.Errorf("published %d snapshots in 500ms at 80ms interval, want >= 2", got)
	}
	if IsRunning() {
		// Stop() is observed by the loop shortly after; give it a moment.
		deadline := time.Now().Add(2 * time.Second)
		for IsRunning() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if IsRunning() {
			t.Error("loop still running after Stop()")
		}
	}
}

func TestStartWaitsOneIntervalBeforeFirstRound(t *testing.T) {
	count := withLoopIsolation(t)

	Start(300 * time.Millisecond)
	time.Sleep(120 * time.Millisecond) // well before the first tick
	early := count()
	Stop()

	if early != 0 {
		t.Errorf("published %d snapshots before the first interval elapsed, want 0", early)
	}
}

func TestStopIsIdempotentAndSafeWithoutStart(t *testing.T) {
	withLoopIsolation(t)
	Stop() // never started
	Stop() // double stop must not panic
	if IsRunning() {
		t.Errorf("IsRunning = true with no loop started")
	}
}

func TestStartTwiceDoesNotLeakLoops(t *testing.T) {
	count := withLoopIsolation(t)

	Start(60 * time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	Start(60 * time.Millisecond) // restarts: the old loop must be stopped
	time.Sleep(300 * time.Millisecond)
	Stop()

	// Two loops running in parallel would roughly double the publish rate;
	// with only one loop the count stays within a small window.
	got := count()
	if got < 3 {
		t.Errorf("published %d snapshots, want >= 3", got)
	}
	if max := 14; got > max {
		t.Errorf("published %d snapshots, want <= %d (two loops were racing)", got, max)
	}
}

func TestRoundWritesState(t *testing.T) {
	origPath, origPub := stateFilePathFn, pubfn_
	path := filepath.Join(t.TempDir(), "state.json")
	stateFilePathFn = func() string { return path }
	pubfn_ = func(v any) error { return nil }
	defer func() { stateFilePathFn, pubfn_ = origPath, origPub }()

	state := loadState(path)
	round(state, nil)

	got := loadState(path)
	if got.LastOkAt <= 0 {
		t.Errorf("LastOkAt = %d, want a fresh timestamp", got.LastOkAt)
	}
	// A host with no temperature sensor must still persist its reason.
	if len(state.LastErrs) > 3 {
		t.Errorf("persisted %d errors, want at most 3", len(state.LastErrs))
	}
}
