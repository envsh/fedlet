package xhs

import (
	"encoding/json"
	"testing"
)

func TestDESEncryptRoundTrip(t *testing.T) {
	key := []byte("WquqhEkd")
	for _, in := range []string{"10", "257", "short", "a rather longer payload that exceeds one block boundary boundary"} {
		enc, err := desECBEncrypt([]byte(in), key)
		if err != nil {
			t.Fatalf("encrypt %q: %v", in, err)
		}
		if len(enc)%8 != 0 {
			t.Fatalf("encrypt %q: length %d not block multiple", in, len(enc))
		}
		dec, err := desECBDecrypt(enc, key)
		if err != nil {
			t.Fatalf("decrypt %q: %v", in, err)
		}
		if string(dec) != in {
			t.Fatalf("round trip %q got %q", in, dec)
		}
	}
}

func TestGenerateTrackShape(t *testing.T) {
	raw := generateTrack(60)
	var triples [][3]int
	if err := json.Unmarshal(raw, &triples); err != nil {
		t.Fatalf("generateTrack output not JSON [[x,y,z]...]: %v (%s)", err, raw)
	}
	if len(triples) != 30 { // 60px / 2 per step
		t.Fatalf("generated %d points, want 30", len(triples))
	}
	// x strictly increases by 2, monotonically.
	for i, p := range triples {
		if p[0] != 2*(i+1) {
			t.Fatalf("point %d x=%d, want %d", i, p[0], 2*(i+1))
		}
	}
}

func TestParseAngleConclusion(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want float64
		ok   bool
	}{
		{in: "127", want: 127, ok: true},
		{in: " 88.5 ", want: 88.5, ok: true},
		{in: "360", want: 360, ok: true},
		{in: "0", ok: false},
		{in: "361", ok: false},
		{in: "-5", ok: false},
		{in: "abc", ok: false},
		{in: "", ok: false},
	} {
		got, ok := parseAngleConclusion(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Fatalf("parseAngleConclusion(%q) = (%v,%v), want (%v,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
