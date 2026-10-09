package sysinfo

import (
	"testing"
)

// ioregArraySample is the shape `ioreg -a` emits on Apple Silicon: an array of
// one dict with integer values only. Field names must match the registry keys
// exactly, so this doubles as a regression test for the missing-plist-tag
// mapping and for integer-number decoding (a plain float64 field used to abort
// with "type mismatch" here).
const ioregArraySample = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<array>
	<dict>
		<key>AppleRawCurrentCapacity</key><integer>25000</integer>
		<key>AppleRawMaxCapacity</key><integer>50000</integer>
		<key>DesignCapacity</key><integer>60000</integer>
		<key>Voltage</key><integer>11400</integer>
		<key>Amperage</key><integer>-1200</integer>
		<key>CycleCount</key><integer>456</integer>
		<key>Temperature</key><integer>2956</integer>
		<key>IsCharging</key><false/>
		<key>ExternalConnected</key><false/>
		<key>FullyCharged</key><false/>
		<key>PermanentFailureStatus</key><integer>0</integer>
	</dict>
</array>
</plist>`

// ioregDictSample is the bare-dict shape some Intel machines emit.
const ioregDictSample = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>AppleRawCurrentCapacity</key><integer>1000</integer>
	<key>AppleRawMaxCapacity</key><integer>2000</integer>
	<key>DesignCapacity</key><integer>2500</integer>
	<key>Voltage</key><integer>12000</integer>
	<key>Amperage</key><integer>500</integer>
	<key>CycleCount</key><integer>10</integer>
	<key>IsCharging</key><true/>
	<key>ExternalConnected</key><true/>
	<key>FullyCharged</key><false/>
</dict>
</plist>`

// ioregRealSample covers a driver/OS that publishes a value as <real> instead
// of <integer>: ioregNum must accept both.
const ioregRealSample = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<array>
	<dict>
		<key>AppleRawCurrentCapacity</key><integer>1000</integer>
		<key>AppleRawMaxCapacity</key><integer>2000</integer>
		<key>Voltage</key><real>12000.0</real>
		<key>Temperature</key><real>2956.5</real>
	</dict>
</array>
</plist>`

func TestDecodeIoregPlistArrayShape(t *testing.T) {
	raws, err := decodeIoregPlist([]byte(ioregArraySample))
	if err != nil {
		t.Fatalf("decode array shape: %v", err)
	}
	if len(raws) != 1 {
		t.Fatalf("got %d entries, want 1", len(raws))
	}
	raw := raws[0]
	if raw.AppleRawCurrentCapacity != 25000 {
		t.Errorf("AppleRawCurrentCapacity = %v, want 25000 (plist field mapping broken)", raw.AppleRawCurrentCapacity)
	}
	if raw.CycleCount != 456 {
		t.Errorf("CycleCount = %v, want 456", raw.CycleCount)
	}
	if raw.IsCharging {
		t.Errorf("IsCharging = true, want false")
	}
}

func TestDecodeIoregPlistDictShape(t *testing.T) {
	raws, err := decodeIoregPlist([]byte(ioregDictSample))
	if err != nil {
		t.Fatalf("decode dict shape: %v", err)
	}
	if len(raws) != 1 || raws[0].CycleCount != 10 {
		t.Fatalf("dict shape not decoded: %+v", raws)
	}
	if !raws[0].IsCharging {
		t.Error("IsCharging lost in dict shape")
	}
}

func TestDecodeIoregPlistRealNumbers(t *testing.T) {
	raws, err := decodeIoregPlist([]byte(ioregRealSample))
	if err != nil {
		t.Fatalf("decode real shape: %v", err)
	}
	if len(raws) != 1 {
		t.Fatalf("got %d entries, want 1", len(raws))
	}
	if float64(raws[0].Temperature) != 2956.5 {
		t.Errorf("Temperature = %v, want 2956.5", raws[0].Temperature)
	}
	if float64(raws[0].Voltage) != 12000 {
		t.Errorf("Voltage = %v, want 12000", raws[0].Voltage)
	}
}

// ioregModernSample reproduces newer macOS, where the top-level
// AppleRaw*Capacity keys are gone, MaxCapacity is the 100 service flag and the
// real figures only exist inside the nested BatteryData blob (stats #3392).
const ioregModernSample = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<array>
	<dict>
		<key>MaxCapacity</key><integer>100</integer>
		<key>CurrentCapacity</key><integer>84</integer>
		<key>DesignCapacity</key><integer>6075</integer>
		<key>Voltage</key><integer>11500</integer>
		<key>Amperage</key><integer>-900</integer>
		<key>IsCharging</key><false/>
		<key>ExternalConnected</key><false/>
		<key>BatteryData</key>
		<dict>
			<key>CycleCount</key><integer>312</integer>
			<key>DesignCapacity</key><integer>6075</integer>
			<key>RemainingCapacity</key><integer>4918</integer>
			<key>NominalChargeCapacity</key><integer>5118</integer>
			<key>FullChargeCapacity</key><integer>5090</integer>
		</dict>
	</dict>
</array>
</plist>`

func TestConvertIoregNestedBatteryDataFallback(t *testing.T) {
	raws, err := decodeIoregPlist([]byte(ioregModernSample))
	if err != nil {
		t.Fatalf("decode modern shape: %v", err)
	}
	b := convertIoreg(raws[0])
	// MaxCapacity=100 is the Apple silicon service flag, never a capacity: the
	// nested blob must win.
	if b.Full != 5118 {
		t.Errorf("Full = %v, want 5118 from BatteryData.NominalChargeCapacity", b.Full)
	}
	if b.Current != 4918 {
		t.Errorf("Current = %v, want 4918 from BatteryData.RemainingCapacity", b.Current)
	}
	if b.Design != 6075 {
		t.Errorf("Design = %v, want 6075", b.Design)
	}
	if b.Cycles != 312 {
		t.Errorf("Cycles = %v, want 312 from BatteryData.CycleCount", b.Cycles)
	}
	if b.HealthPercent < 84.2 || b.HealthPercent > 84.3 {
		t.Errorf("HealthPercent = %v, want ~84.24", b.HealthPercent)
	}
}

func TestDecodeIoregPlistGarbage(t *testing.T) {
	if _, err := decodeIoregPlist([]byte("not a plist")); err == nil {
		t.Error("garbage input must produce an error (rule E)")
	}
}

func TestConvertIoregDischarging(t *testing.T) {
	b := convertIoreg(ioregBattery{
		AppleRawCurrentCapacity: 25000,
		AppleRawMaxCapacity:     50000,
		DesignCapacity:          60000,
		Voltage:                 11400, // mV
		Amperage:                -1200, // mA
		CycleCount:              456,
		Temperature:             2956, // 0.1 K
	})

	if b.Percent != 50 {
		t.Errorf("Percent = %v, want 50", b.Percent)
	}
	if b.Full != 50000 || b.Design != 60000 || b.Current != 25000 {
		t.Errorf("capacity = %v/%v/%v, want 50000/60000/25000", b.Full, b.Design, b.Current)
	}
	if b.Voltage != 11.4 {
		t.Errorf("Voltage = %v, want 11.4 V (mV → V)", b.Voltage)
	}
	if b.Cycles != 456 {
		t.Errorf("Cycles = %v, want 456", b.Cycles)
	}
	// 2956/10 − 273.15 = 22.45 °C
	if b.Temperature < 22.4 || b.Temperature > 22.5 {
		t.Errorf("Temperature = %v, want ~22.45 (0.1 K → °C)", b.Temperature)
	}
	// Discharging: rate must be negative even though Amperage is already < 0.
	if b.ChargeRate >= 0 {
		t.Errorf("ChargeRate = %v, want negative while discharging", b.ChargeRate)
	}
	if b.Charging != TriNo || b.ACPower != TriNo {
		t.Errorf("Charging/AC = %d/%d, want 0/0", b.Charging, b.ACPower)
	}
	if b.State != "Discharging" {
		t.Errorf("State = %q, want Discharging", b.State)
	}
	if b.HealthPercent < 83.3 || b.HealthPercent > 83.4 {
		t.Errorf("HealthPercent = %v, want ~83.33", b.HealthPercent)
	}
}

func TestConvertIoregCharging(t *testing.T) {
	b := convertIoreg(ioregBattery{
		AppleRawCurrentCapacity: 1000,
		AppleRawMaxCapacity:     2000,
		DesignCapacity:          2500,
		Voltage:                 12000,
		Amperage:                500,
		CycleCount:              10,
		IsCharging:              true,
		ExternalConnected:       true,
	})
	if b.Charging != TriYes || b.ACPower != TriYes {
		t.Errorf("Charging/AC = %d/%d, want 1/1", b.Charging, b.ACPower)
	}
	if b.ChargeRate <= 0 {
		t.Errorf("ChargeRate = %v, want positive while charging", b.ChargeRate)
	}
	if b.State != "Charging" {
		t.Errorf("State = %q, want Charging", b.State)
	}
}

func TestConvertIoregUnsupportedFieldsAreSentinel(t *testing.T) {
	// A driver that reports no cycle counter, no design capacity and no
	// temperature must leave -1 behind rather than zero.
	b := convertIoreg(ioregBattery{
		AppleRawCurrentCapacity: 500,
		AppleRawMaxCapacity:     1000,
	})
	if b.Cycles != InvalidNum {
		t.Errorf("Cycles = %v, want -1", b.Cycles)
	}
	if b.Design != InvalidNum {
		t.Errorf("Design = %v, want -1", b.Design)
	}
	if b.Temperature != InvalidNum {
		t.Errorf("Temperature = %v, want -1", b.Temperature)
	}
	if b.Voltage != InvalidNum {
		t.Errorf("Voltage = %v, want -1", b.Voltage)
	}
	if b.ChargeRate != InvalidNum {
		t.Errorf("ChargeRate = %v, want -1", b.ChargeRate)
	}
	if b.Health != "" {
		t.Errorf("Health = %q, want empty", b.Health)
	}
	// 500/1000 is still computable, so Percent must not be -1.
	if b.Percent != 50 {
		t.Errorf("Percent = %v, want 50", b.Percent)
	}
}

func TestConvertIoregImplausibleTemperatureRejected(t *testing.T) {
	// 0 K and absurd readings are the driver saying "unknown".
	for _, raw := range []ioregNum{0, 500000} {
		b := convertIoreg(ioregBattery{Temperature: raw})
		if b.Temperature != InvalidNum {
			t.Errorf("Temperature(%v) = %v, want -1", raw, b.Temperature)
		}
	}
}

func TestConvertIoregFailedPack(t *testing.T) {
	b := convertIoreg(ioregBattery{PermanentFailureStatus: 4})
	if b.Health != "Failed" {
		t.Errorf("Health = %q, want Failed", b.Health)
	}
}

func TestIoregState(t *testing.T) {
	cases := []struct {
		raw  ioregBattery
		want string
	}{
		{ioregBattery{FullyCharged: true}, "Full"},
		{ioregBattery{IsCharging: true}, "Charging"},
		{ioregBattery{}, "Discharging"},
		{ioregBattery{ExternalConnected: true}, "Idle"},
	}
	for _, c := range cases {
		if got := ioregState(c.raw); got != c.want {
			t.Errorf("ioregState(%+v) = %q, want %q", c.raw, got, c.want)
		}
	}
}
