package sysinfo

// UnifiedMessage (unimsg) conversion: one Snapshot round → one
// fbshared.UnifiedMessage, dual-published alongside the flat P2P payload.
//
// Publish routing (fedbridge/main.go): the flat Snapshot JSON keeps going to
// the P2P topic unchanged, while the UnifiedMessage follows the push-only
// branch (ntfy / mozpush). Neither replaces the other.
//
// Identity is fixed-chat: every host posts into ChatID="sysinfo"; the concrete
// machine is carried by Username/Usernick/AccountName (hostname) and by the
// Text summary's host= field, so multi-host streams stay distinguishable
// without fragmenting the conversation.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

// Fixed conversation identity shared by every host (see file comment).
const (
	unifiedChatID   = "sysinfo"
	unifiedChatName = "主机信息"
)

// ToUnified converts one snapshot into the push form. raw is the marshalled
// Snapshot, attached verbatim so in-process consumers can recover full
// precision that the one-line Text summary omits.
func (s *Snapshot) ToUnified(raw []byte) fbshared.UnifiedMessage {
	return fbshared.UnifiedMessage{
		Text:        summary(s),
		MsgFormat:   fbshared.FmtText,
		Protocol:    fbshared.ProtoSysinfo,
		AccountID:   "", // hostKey(s),
		AccountName: "", // s.Host.Hostname,
		ChatID:      unifiedChatID,
		ChatName:    unifiedChatName,
		Username:    s.Host.Hostname,
		UserID:      hostKey(s),
		Usernick:    s.Host.Hostname,
		MsgType:     fbshared.MsgTypeCreate,
		MsgID:       msgID(s),
		Timestamp:   unifiedTimestamp(s.PublishedAt),
		ChatTopic:   hostTopic(s),
		Raw:         raw,
	}
}

// summary renders the one-line human-readable form:
//
//	host=box cpu=12.3% mem=45.6% (7.3G/16G) load=0.52/0.41/0.3 | / 78.1% | /data 90.0% | batt=92% charging | temp=54C | errs=2
//
// -1 sentinels render as "?"; whole sections without data (no battery, no
// temperature sensor) are omitted rather than showing placeholders.
func summary(s *Snapshot) string {
	var b strings.Builder
	b.WriteString("host=")
	b.WriteString(orQ(s.Host.Hostname))
	b.WriteString(" cpu=")
	b.WriteString(fmtPercent(s.CPU.Percent))
	b.WriteString("% mem=")
	b.WriteString(fmtPercent(s.Mem.UsedPercent))
	b.WriteString("% (")
	b.WriteString(humanBytes(s.Mem.Used))
	b.WriteString("/")
	b.WriteString(humanBytes(s.Mem.Total))
	b.WriteString(") load=")
	b.WriteString(fmtPercent(s.Load.Load1))
	b.WriteString("/")
	b.WriteString(fmtPercent(s.Load.Load5))
	b.WriteString("/")
	b.WriteString(fmtPercent(s.Load.Load15))

	for _, d := range s.Disk {
		b.WriteString(" | ")
		b.WriteString(orQ(d.Mountpoint))
		b.WriteString(" ")
		b.WriteString(fmtPercent(d.UsedPercent))
		b.WriteString("%")
	}

	if bat := pickBattery(s.Battery); bat != nil {
		b.WriteString(" | batt=")
		b.WriteString(fmtPercent(bat.Percent))
		b.WriteString("%")
		if bat.State != "" {
			b.WriteString(" ")
			b.WriteString(strings.ToLower(bat.State))
		}
	}

	if t, ok := maxTemp(s.Temp); ok {
		b.WriteString(" | temp=")
		b.WriteString(trimFloat(t))
		b.WriteString("C")
	}

	if n := len(s.Errors); n > 0 {
		b.WriteString(" | errs=")
		b.WriteString(strconv.Itoa(n))
	}
	return b.String()
}

// hostKey is the stable identity chain: HostID is gopsutil's machine id and is
// the only value suitable for cross-restart keys; hostname is the readable
// fallback; "localhost" keeps the field non-empty when both are missing.
func hostKey(s *Snapshot) string {
	if s.Host.HostID != "" {
		return s.Host.HostID
	}
	if s.Host.Hostname != "" {
		return s.Host.Hostname
	}
	return "localhost"
}

// msgID is unique per round and stable for a given PublishedAt.
func msgID(s *Snapshot) string {
	at := s.PublishedAt
	if at <= 0 {
		at = time.Now().Unix()
	}
	return fmt.Sprintf("sysinfo-%d", at)
}

// unifiedTimestamp converts the wire's Unix seconds to the UnixNano the
// UnifiedMessage convention uses everywhere else; missing time → now.
func unifiedTimestamp(publishedAt int64) int64 {
	if publishedAt <= 0 {
		return time.Now().UnixNano()
	}
	return time.Unix(publishedAt, 0).UnixNano()
}

// hostTopic compresses the machine identity line, dropping unknown segments.
func hostTopic(s *Snapshot) string {
	var parts []string
	for _, v := range []string{s.Host.OS, s.Host.PlatformVersion, s.Host.Kernel, s.Host.Arch} {
		if v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " ")
}

// pickBattery prefers a physically present battery; falls back to the first
// entry so a partially-failed read (rule D) still shows something.
func pickBattery(bats []BatteryInfo) *BatteryInfo {
	for i := range bats {
		if bats[i].Present == TriYes {
			return &bats[i]
		}
	}
	if len(bats) > 0 {
		return &bats[0]
	}
	return nil
}

// maxTemp reports the hottest valid sensor; -1 readings are ignored.
func maxTemp(temps []TempInfo) (float64, bool) {
	var (
		maxv float64
		ok   bool
	)
	for _, t := range temps {
		if t.Celsius == InvalidNum {
			continue
		}
		if !ok || t.Celsius > maxv {
			maxv, ok = t.Celsius, true
		}
	}
	return maxv, ok
}

// humanBytes renders a byte count with a K/M/G/T suffix; -1 → "?".
func humanBytes(v float64) string {
	if v == InvalidNum {
		return "?"
	}
	const unit = 1024
	if v < unit {
		return fmt.Sprintf("%dB", int64(v))
	}
	div, exp := float64(unit), 0
	for n := v / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", v/div, "KMGTPE"[exp])
}

// orQ substitutes the empty-string unknown marker with "?" for the summary.
func orQ(v string) string {
	if v == "" {
		return "?"
	}
	return v
}
