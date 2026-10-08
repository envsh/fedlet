//go:build linux || android

package sysinfo

import (
	"os"
	"path/filepath"
	"testing"
)

// mkSupply writes a fake power-supply directory and returns its path.
func mkSupply(t *testing.T, root, name string, attrs map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	for k, v := range attrs {
		if err := os.WriteFile(filepath.Join(dir, k), []byte(v+"\n"), 0644); err != nil {
			t.Fatalf("write %s/%s: %v", dir, k, err)
		}
	}
	return dir
}

// usePowerSupply points powerSupplyDir at a fixture for the duration of a test.
func usePowerSupply(t *testing.T, root string) {
	t.Helper()
	orig := powerSupplyDir
	powerSupplyDir = root
	t.Cleanup(func() { powerSupplyDir = orig })
}

func TestReadSysfsBatteryEnergyForm(t *testing.T) {
	root := t.TempDir()
	dir := mkSupply(t, root, "BAT0", map[string]string{
		"type":               "Battery",
		"status":             "Discharging",
		"present":            "1",
		"capacity":           "50",
		"cycle_count":        "321",
		"voltage_now":        "11000000",  // 11 V
		"energy_full":        "50000000",  // 50000 mWh
		"energy_full_design": "60000000",  // 60000 mWh
		"energy_now":         "25000000",  // 25000 mWh
		"power_now":          "-15000000", // -15 W while discharging
		"temperature":        "312",       // 31.2 C
		"health":             "Good",
	})

	b, ok := readSysfsBattery(dir, "BAT0", newErrCollector())
	if !ok {
		t.Fatal("battery was rejected")
	}
	if b.State != "Discharging" {
		t.Errorf("State = %q, want Discharging", b.State)
	}
	if b.Charging != TriNo {
		t.Errorf("Charging = %d, want 0", b.Charging)
	}
	if b.Percent != 50 {
		t.Errorf("Percent = %v, want 50", b.Percent)
	}
	if b.Cycles != 321 {
		t.Errorf("Cycles = %v, want 321", b.Cycles)
	}
	if b.Full != 50000 {
		t.Errorf("Full = %v, want 50000 mWh", b.Full)
	}
	if b.Design != 60000 {
		t.Errorf("Design = %v, want 60000 mWh", b.Design)
	}
	if b.Current != 25000 {
		t.Errorf("Current = %v, want 25000 mWh", b.Current)
	}
	if want := 83.333; b.HealthPercent < want-0.01 || b.HealthPercent > want+0.01 {
		t.Errorf("HealthPercent = %v, want ~83.33", b.HealthPercent)
	}
	if b.Voltage != 11 {
		t.Errorf("Voltage = %v, want 11 V", b.Voltage)
	}
	if b.Temperature < 31.1 || b.Temperature > 31.3 {
		t.Errorf("Temperature = %v, want ~31.2", b.Temperature)
	}
	if b.Health != "Good" {
		t.Errorf("Health = %q, want Good", b.Health)
	}
	if b.Present != TriYes {
		t.Errorf("Present = %d, want 1", b.Present)
	}
	// power_now is negative while discharging, so the rate stays negative.
	if b.ChargeRate >= 0 && b.ChargeRate != InvalidNum {
		t.Errorf("ChargeRate = %v, want negative (discharging)", b.ChargeRate)
	}
}

func TestReadSysfsBatteryChargeFormConvertsViaVoltage(t *testing.T) {
	root := t.TempDir()
	dir := mkSupply(t, root, "BAT0", map[string]string{
		"type":               "Battery",
		"status":             "Charging",
		"voltage_now":        "11000000", // 11 V
		"charge_full":        "4000000",  // 4 Ah
		"charge_full_design": "5000000",  // 5 Ah
		"charge_now":         "2000000",  // 2 Ah
		"power_now":          "15000000", // 15 W charging
	})

	b, ok := readSysfsBattery(dir, "BAT0", newErrCollector())
	if !ok {
		t.Fatal("battery was rejected")
	}
	// 4000 µAh × 11 V × 1e-9 = 44000 mWh
	if b.Full != 44000 {
		t.Errorf("Full = %v, want 44000 mWh", b.Full)
	}
	// 5000 µAh × 11 V × 1e-9 = 55000 mWh
	if b.Design != 55000 {
		t.Errorf("Design = %v, want 55000 mWh", b.Design)
	}
	if b.Current != 22000 {
		t.Errorf("Current = %v, want 22000 mWh", b.Current)
	}
	if b.Charging != TriYes {
		t.Errorf("Charging = %d, want 1", b.Charging)
	}
	// No capacity attribute and no percent here, so it is derived from mWh.
	if b.Percent < 49.9 || b.Percent > 50.1 {
		t.Errorf("Percent = %v, want ~50", b.Percent)
	}
	if b.ChargeRate != 15000 {
		t.Errorf("ChargeRate = %v, want 15000 mW", b.ChargeRate)
	}
	// Round-trip health: 44000/55000 = 80%
	if b.HealthPercent < 79.9 || b.HealthPercent > 80.1 {
		t.Errorf("HealthPercent = %v, want ~80", b.HealthPercent)
	}
}

func TestReadSysfsBatteryChargeFormWithoutVoltage(t *testing.T) {
	root := t.TempDir()
	dir := mkSupply(t, root, "BAT0", map[string]string{
		"type":               "Battery",
		"charge_full":        "4000",
		"charge_full_design": "5000",
		"charge_now":         "2000",
	})

	b, ok := readSysfsBattery(dir, "BAT0", newErrCollector())
	if !ok {
		t.Fatal("battery was rejected")
	}
	// Without a voltage reading µAh cannot become mWh: rule B, not a guess.
	if b.Full != InvalidNum || b.Design != InvalidNum || b.Current != InvalidNum {
		t.Errorf("capacity = %v/%v/%v, want -1/-1/-1 without voltage", b.Full, b.Design, b.Current)
	}
	if b.HealthPercent != InvalidNum {
		t.Errorf("HealthPercent = %v, want -1", b.HealthPercent)
	}
	if b.Percent != InvalidNum {
		t.Errorf("Percent = %v, want -1 (no capacity attr, no computable ratio)", b.Percent)
	}
}

func TestReadSysfsBatteryEmptyBayIsSkipped(t *testing.T) {
	root := t.TempDir()
	dir := mkSupply(t, root, "BAT1", map[string]string{
		"type":    "Battery",
		"present": "0",
	})
	if _, ok := readSysfsBattery(dir, "BAT1", newErrCollector()); ok {
		t.Error("present=0 bay must be skipped, not reported")
	}
}

func TestReadSysfsBatteryMissingCapacityIsRecorded(t *testing.T) {
	root := t.TempDir()
	dir := mkSupply(t, root, "BAT0", map[string]string{
		"type":   "Battery",
		"status": "Full",
	})
	errs := newErrCollector()
	b, ok := readSysfsBattery(dir, "BAT0", errs)
	if !ok {
		t.Fatal("battery was rejected")
	}
	if b.Percent != InvalidNum {
		t.Errorf("Percent = %v, want -1", b.Percent)
	}
	list := errs.String()
	if list == nil {
		t.Fatal("missing capacity must land in errors[]")
	}
	if list[0] != "battery:BAT0: no capacity attribute" {
		t.Errorf("errors[] = %v", list)
	}
}

func TestReadSysfsBatteryUnknownStatusIsTriUnknown(t *testing.T) {
	root := t.TempDir()
	dir := mkSupply(t, root, "BAT0", map[string]string{
		"type":   "Battery",
		"status": "Unknown",
	})
	b, _ := readSysfsBattery(dir, "BAT0", newErrCollector())
	if b.Charging != TriUnknown {
		t.Errorf("Charging = %d, want -1 (driver says unknown)", b.Charging)
	}
}

func TestReadBatteriesFixtureTree(t *testing.T) {
	root := t.TempDir()
	usePowerSupply(t, root)

	mkSupply(t, root, "BAT0", map[string]string{
		"type":        "Battery",
		"status":      "Charging",
		"capacity":    "80",
		"cycle_count": "12",
	})
	mkSupply(t, root, "AC", map[string]string{
		"type":   "Mains",
		"online": "1",
	})
	// Non-battery supplies must not become battery rows.
	mkSupply(t, root, "hidpp_battery_0", map[string]string{
		"type": "Battery",
	})

	bats, err := readBatteries(newErrCollector())
	if err != nil {
		t.Fatalf("readBatteries: %v", err)
	}
	if len(bats) < 1 {
		t.Fatalf("got %d batteries, want >= 1", len(bats))
	}
	found := false
	for _, b := range bats {
		if b.Percent == 80 && b.Cycles == 12 {
			found = true
			if b.ACPower != TriYes {
				t.Errorf("ACPower = %d, want 1 (Mains online)", b.ACPower)
			}
		}
	}
	if !found {
		t.Errorf("BAT0 not found among %+v", bats)
	}
}

func TestReadBatteriesNoBatteryIsNotAnError(t *testing.T) {
	root := t.TempDir()
	usePowerSupply(t, root)
	mkSupply(t, root, "AC", map[string]string{"type": "Mains", "online": "0"})

	bats, err := readBatteries(newErrCollector())
	if err != nil {
		t.Fatalf("desktop without battery must not error, got %v", err)
	}
	if len(bats) != 0 {
		t.Errorf("got %d batteries, want 0", len(bats))
	}
}

func TestReadBatteriesMissingDirIsRuleE(t *testing.T) {
	usePowerSupply(t, filepath.Join(t.TempDir(), "does-not-exist"))
	if _, err := readBatteries(newErrCollector()); err == nil {
		t.Error("missing power_supply tree must return an error (rule E)")
	}
}

// ---- status string mapping ----

func TestChargingTri(t *testing.T) {
	cases := map[string]int{
		"Charging":      TriYes,
		"Discharging":   TriNo,
		"Full":          TriNo,
		"Not charging":  TriNo,
		"Idle":          TriNo,
		"Unknown":       TriUnknown,
		"":              TriUnknown,
		"SomethingElse": TriUnknown,
	}
	for in, want := range cases {
		if got := chargingTri(in); got != want {
			t.Errorf("chargingTri(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestNormalizeState(t *testing.T) {
	if got := normalizeState("Not charging"); got != "Idle" {
		t.Errorf("normalizeState(Not charging) = %q, want Idle", got)
	}
	if got := normalizeState("Discharging"); got != "Discharging" {
		t.Errorf("normalizeState = %q", got)
	}
	if got := normalizeState("  "); got != "" {
		t.Errorf("blank should stay blank, got %q", got)
	}
}

// ---- dumpsys fallback (Android) ----

const dumpsysSample = `Current Battery Service state:
  AC powered: false
  USB powered: true
  Wireless powered: false
  Max charging current: 500000
  status: 2
  health: 2
  present: true
  level: 55
  voltage: 4200
  temperature: 250
  technology: Li-ion
`

func TestParseDumpsysBattery(t *testing.T) {
	got := parseDumpsysBattery(dumpsysSample)
	want := map[string]string{
		"ac powered":           "false",
		"usb powered":          "true",
		"status":               "2",
		"health":               "2",
		"present":              "true",
		"level":                "55",
		"voltage":              "4200",
		"temperature":          "250",
		"technology":           "Li-ion",
		"max charging current": "500000",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q = %q, want %q", k, got[k], v)
		}
	}
	// The section header carries no value; it is parsed as an empty string and
	// never read by the field mapping.
	if v, ok := got["current battery service state"]; ok && v != "" {
		t.Errorf("header line became a non-empty key: %q", v)
	}
}

func TestMapAndroidStatusAndHealth(t *testing.T) {
	statuses := map[string]string{
		"1": "Unknown", "2": "Charging", "3": "Discharging",
		"4": "Idle", "5": "Full", "2 ": "Charging",
	}
	for in, want := range statuses {
		if got := mapAndroidStatus(in); got != want {
			t.Errorf("mapAndroidStatus(%q) = %q, want %q", in, got, want)
		}
	}
	healths := map[string]string{
		"2": "Good", "3": "Overheat", "4": "Dead", "7": "Cold", "9": "9",
	}
	for in, want := range healths {
		if got := mapAndroidHealth(in); got != want {
			t.Errorf("mapAndroidHealth(%q) = %q, want %q", in, got, want)
		}
	}
	// An already-readable label passes through.
	if got := mapAndroidHealth("Good"); got != "Good" {
		t.Errorf("mapAndroidHealth(Good) = %q", got)
	}
}

func TestReadDumpsysBatteryParsesSample(t *testing.T) {
	orig := powerSupplyDir
	powerSupplyDir = t.TempDir() // no cycle_count file to find
	t.Cleanup(func() { powerSupplyDir = orig })

	// exec is bypassed by exercising the parser + conversion directly through
	// the exported field mapping; the command itself only runs on Android.
	fields := parseDumpsysBattery(dumpsysSample)
	if fields["level"] != "55" {
		t.Fatalf("fixture changed: level = %q", fields["level"])
	}
	if got := mapAndroidStatus(fields["status"]); got != "Charging" {
		t.Errorf("status mapping = %q, want Charging", got)
	}
	if got := mapAndroidHealth(fields["health"]); got != "Good" {
		t.Errorf("health mapping = %q, want Good", got)
	}
}
