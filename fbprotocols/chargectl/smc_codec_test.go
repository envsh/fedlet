package chargectl

import (
	"bytes"
	"testing"
)

func TestFourCC(t *testing.T) {
	cases := []struct {
		in   string
		want uint32
		ok   bool
	}{
		{"BCLM", 0x42434C4D, true},
		{"CH0B", 0x43483042, true},
		{"A", 0x41000000, true},
		{"", 0, false},
		{"ABCDE", 0, false},
	}
	for _, c := range cases {
		got, ok := fourCC(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("fourCC(%q)=(%#x,%v), want (%#x,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestKeyName(t *testing.T) {
	if got := keyName(0x42434C4D); got != "BCLM" {
		t.Fatalf("keyName=%q, want BCLM", got)
	}
}

func TestU32RoundTrip(t *testing.T) {
	b := make([]byte, 4)
	putU32(b, 0, 0xDEADBEEF)
	if got := getU32(b, 0); got != 0xDEADBEEF {
		t.Fatalf("getU32=%#x", got)
	}
}

func TestHexBytes(t *testing.T) {
	if got := hexBytes([]byte{0x02}); got != "02" {
		t.Fatalf("hexBytes=%q", got)
	}
	if got := hexBytes([]byte{0x01, 0x00, 0x00, 0x00}); got != "01000000" {
		t.Fatalf("hexBytes=%q", got)
	}
}

func TestSmcStopAllow(t *testing.T) {
	cases := []struct {
		key         string
		stop, allow []byte
	}{
		{"CH0B", []byte{0x02}, []byte{0x00}},
		{"CHTE", []byte{0x01, 0x00, 0x00, 0x00}, []byte{0x00, 0x00, 0x00, 0x00}},
		{"CHWA", []byte{0x01}, []byte{0x00}},
		{"CH0I", []byte{0x01}, []byte{0x00}},
		{"CH0J", []byte{0x01}, []byte{0x00}},
		{"CH0K", []byte{0x01}, []byte{0x00}},
		{"CHIE", []byte{0x08}, []byte{0x00}},
	}
	for _, c := range cases {
		stop, allow := smcStopAllow(c.key)
		if !bytes.Equal(stop, c.stop) || !bytes.Equal(allow, c.allow) {
			t.Errorf("smcStopAllow(%s)=(%x,%x), want (%x,%x)", c.key, stop, allow, c.stop, c.allow)
		}
	}
}

func TestLE32RoundTrip(t *testing.T) {
	if got := le32(le32b(50)); got != 50 {
		t.Fatalf("le32 round trip=%d, want 50", got)
	}
	b := le32b(50)
	if b[0] != 50 || b[1] != 0 || b[2] != 0 || b[3] != 0 {
		t.Fatalf("le32b(50)=%x, want 32 00 00 00", b)
	}
}

// The native path depends on these exact offsets into the 80-byte struct.
func TestSMCLayout(t *testing.T) {
	if smcKeyDataSize != 80 || offKey != 0 || offKeyInfoSize != 28 ||
		offData8 != 42 || offBytes != 48 {
		t.Fatalf("SMC layout changed: size=%d key=%d sizeOff=%d data8=%d bytes=%d",
			smcKeyDataSize, offKey, offKeyInfoSize, offData8, offBytes)
	}
}
