package chargectl

import "testing"

// Intel: CurrentCapacity/MaxCapacity are mAh, so the percentage is derived.
func TestParseIoregBattery(t *testing.T) {
	out := "\"BatteryInstalled\" = Yes\n" +
		"\"CurrentCapacity\" = 3000\n" +
		"\"MaxCapacity\" = 4000\n" +
		"\"ExternalConnected\" = Yes\n" +
		"\"IsCharging\" = No\n"
	b := parseIoregBattery(out)
	if !b.installed || !b.externalPower || b.charging {
		t.Fatalf("flags wrong: %+v", b)
	}
	if b.capacity != 75 {
		t.Fatalf("capacity=%d, want 75", b.capacity)
	}
}

// Apple silicon: MaxCapacity is 100 and CurrentCapacity is already a percent.
func TestParseIoregBatteryAS(t *testing.T) {
	out := "\"BatteryInstalled\" = Yes\n" +
		"\"CurrentCapacity\" = 80\n" +
		"\"MaxCapacity\" = 100\n" +
		"\"ExternalConnected\" = Yes\n" +
		"\"IsCharging\" = Yes\n"
	b := parseIoregBattery(out)
	if b.capacity != 80 || !b.charging {
		t.Fatalf("got %+v", b)
	}
}

// pmset fallback: header carries AC/Battery and the row carries the percent.
func TestParsePmset(t *testing.T) {
	out := "Now drawing from 'AC Power'\n" +
		" -InternalBattery-0 (id=1)\t78%; charged; 0:00 remaining present: true\n"
	b := parsePmsetBatt(out)
	if b.capacity != 78 || !b.externalPower || !b.fullyCharged {
		t.Fatalf("got %+v", b)
	}
}

func TestBatteryOnAC(t *testing.T) {
	if batteryOnAC(macBattery{}) {
		t.Fatal("all fields false: want false")
	}
	for _, b := range []macBattery{{externalPower: true}, {charging: true}, {fullyCharged: true}} {
		if !batteryOnAC(b) {
			t.Fatalf("%+v: want true", b)
		}
	}
}

// A discharging line must not be read as charging: the substring "charging" is
// itself contained in "discharging".
func TestParsePmsetDischarging(t *testing.T) {
	out := "Now drawing from 'Battery Power'\n" +
		" -InternalBattery-0 (id=1)\t78%; discharging; 4:12 remaining present: true\n"
	b := parsePmsetBatt(out)
	if b.capacity != 78 || b.externalPower || b.charging {
		t.Fatalf("got %+v", b)
	}
}
