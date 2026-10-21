package sysinfo

import (
	"strings"
	"testing"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

func TestToUnifiedFieldMapping(t *testing.T) {
	snap := &Snapshot{
		Kind:        "sysinfo",
		PublishedAt: 1770000000,
		Host: HostInfo{
			Hostname:        "box",
			OS:              "linux",
			PlatformVersion: "12",
			Kernel:          "6.1.0",
			Arch:            "amd64",
			HostID:          "deadbeef",
		},
		CPU:  CPUInfo{Percent: 12.3},
		Mem:  MemInfo{UsedPercent: 45.6, Used: 8 * 1024 * 1024 * 1024, Total: 16 * 1024 * 1024 * 1024},
		Load: LoadInfo{Load1: 0.52, Load5: 0.41, Load15: 0.30},
		Disk: []DiskInfo{
			{Mountpoint: "/", UsedPercent: 78.1},
			{Mountpoint: "/data", UsedPercent: 90.0},
		},
		Battery: []BatteryInfo{
			{Present: TriYes, Percent: 92, State: "Charging"},
		},
		Temp: []TempInfo{{Sensor: "x", Celsius: 54}},
	}

	um := snap.ToUnified([]byte(`{"kind":"sysinfo"}`))

	if um.Protocol != fbshared.ProtoSysinfo {
		t.Errorf("Protocol = %q, want sysinfo", um.Protocol)
	}
	if um.MsgType != "create" {
		t.Errorf("MsgType = %q, want create", um.MsgType)
	}
	if um.MsgFormat != "text" {
		t.Errorf("MsgFormat = %q, want text", um.MsgFormat)
	}
	if um.MsgID != "sysinfo-1770000000" {
		t.Errorf("MsgID = %q, want sysinfo-1770000000", um.MsgID)
	}
	if um.Timestamp != time.Unix(1770000000, 0).UnixNano() {
		t.Errorf("Timestamp = %d, want UnixNano of published_at", um.Timestamp)
	}
	// Fixed chat identity, per-host user identity.
	if um.ChatID != "sysinfo" || um.ChatName == "" {
		t.Errorf("ChatID/ChatName = %q/%q, want sysinfo/<name>", um.ChatID, um.ChatName)
	}
	if um.Username != "box" || um.Usernick != "box" {
		t.Errorf("Username/Usernick = %q/%q, want hostname", um.Username, um.Usernick)
	}
	if um.AccountID != "deadbeef" || um.UserID != "deadbeef" {
		t.Errorf("AccountID/UserID = %q/%q, want HostID", um.AccountID, um.UserID)
	}
	if um.AccountName != "box" {
		t.Errorf("AccountName = %q, want box", um.AccountName)
	}
	if um.ChatTopic != "linux 12 6.1.0 amd64" {
		t.Errorf("ChatTopic = %q, want os/platform/kernel/arch", um.ChatTopic)
	}
	if string(um.Raw) != `{"kind":"sysinfo"}` {
		t.Errorf("Raw = %q, want the marshalled snapshot", um.Raw)
	}
	// Icons/gateway stay empty: a host has no avatar.
	if um.UserIcon != "" || um.ChanIcon != "" || um.Gateway != "" {
		t.Errorf("icons/gateway should stay empty, got %q/%q/%q",
			um.UserIcon, um.ChanIcon, um.Gateway)
	}
}

func TestToUnifiedSummary(t *testing.T) {
	snap := &Snapshot{
		Host: HostInfo{Hostname: "box"},
		CPU:  CPUInfo{Percent: 12.3},
		Mem:  MemInfo{UsedPercent: 45.6, Used: 8 * 1024 * 1024 * 1024, Total: 16 * 1024 * 1024 * 1024},
		Load: LoadInfo{Load1: 0.52, Load5: 0.41, Load15: 0.30},
		Disk: []DiskInfo{{Mountpoint: "/", UsedPercent: 78.1}},
		Battery: []BatteryInfo{
			{Present: TriYes, Percent: 92, State: "Charging"},
		},
		Temp:   []TempInfo{{Sensor: "x", Celsius: 54}},
		Errors: []string{"net: boom"},
	}
	text := snap.ToUnified(nil).Text

	for _, want := range []string{
		"host=box", "cpu=12.3%", "mem=45.6%", "8.0G/16.0G",
		"load=0.52/0.41/0.3", "/ 78.1%", "batt=92% charging", "temp=54C", "errs=1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("summary %q missing %q", text, want)
		}
	}
}

func TestToUnifiedSummarySentinelsAndOmissions(t *testing.T) {
	// First-round snapshot: rates -1, no battery, no temp sensor.
	snap := &Snapshot{
		Host: HostInfo{Hostname: "srv"},
		CPU:  CPUInfo{Percent: InvalidNum},
		Mem:  MemInfo{UsedPercent: InvalidNum, Used: InvalidNum, Total: InvalidNum},
		Load: LoadInfo{Load1: InvalidNum, Load5: InvalidNum, Load15: InvalidNum},
	}
	text := snap.ToUnified(nil).Text

	if !strings.Contains(text, "cpu=?%") {
		t.Errorf("summary %q: -1 cpu should render as ?", text)
	}
	if !strings.Contains(text, "(?/?)") {
		t.Errorf("summary %q: -1 mem bytes should render as ?", text)
	}
	if strings.Contains(text, "batt=") {
		t.Errorf("summary %q: no-battery snapshot must omit batt section", text)
	}
	if strings.Contains(text, "temp=") {
		t.Errorf("summary %q: no-temp snapshot must omit temp section", text)
	}
	if strings.Contains(text, "errs=") {
		t.Errorf("summary %q: no errors must omit errs section", text)
	}
	// Absent hostname renders as ? rather than empty.
	if !strings.Contains(text, "host=srv") {
		t.Errorf("summary %q missing host=srv", text)
	}
	snap.Host.Hostname = ""
	if !strings.Contains(snap.ToUnified(nil).Text, "host=?") {
		t.Errorf("empty hostname should render as host=?")
	}
}

func TestToUnifiedIdentityFallbacks(t *testing.T) {
	// HostID empty → hostname; both empty → localhost.
	um := (&Snapshot{Host: HostInfo{Hostname: "box"}}).ToUnified(nil)
	if um.AccountID != "box" || um.UserID != "box" {
		t.Errorf("AccountID/UserID = %q/%q, want hostname fallback", um.AccountID, um.UserID)
	}
	um = (&Snapshot{}).ToUnified(nil)
	if um.AccountID != "localhost" || um.UserID != "localhost" {
		t.Errorf("AccountID/UserID = %q/%q, want localhost fallback", um.AccountID, um.UserID)
	}
}

func TestToUnifiedTimestampFallback(t *testing.T) {
	before := time.Now().UnixNano()
	um := (&Snapshot{}).ToUnified(nil)
	if um.Timestamp < before {
		t.Errorf("Timestamp = %d, want ~now when published_at is missing", um.Timestamp)
	}
}
