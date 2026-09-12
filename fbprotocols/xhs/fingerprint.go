package xhs

// x-s-common b1 fingerprint generation (ported from xhshow 0.2.0).
//
// b1 is the ARC4-encrypted subset of the browser fingerprint JSON, then
// url-quoted, "unquoted" back into bytes, and custom-base64-encoded. Only the
// subset fields below feed b1 (x33..x82) — the rest of the web-facing
// fingerprint is not transmitted and is intentionally omitted.
//
// The url-quote/unquote round-trip in the reference implementation is a
// byte-exact identity when the JSON starts with '{' (it does), so we replay it
// verbatim to stay byte-faithful to the reference.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// b1Payload mirrors xhshow.fingerprint.generate_b1's field subset, in the
// exact JSON key order the reference produces.
type b1Payload struct {
	X33 string `json:"x33"` // "0"
	X34 string `json:"x34"` // "0"
	X35 string `json:"x35"` // "0"
	X36 string `json:"x36"` // random 1..20
	X37 string `json:"x37"` // canvas flags (fixed)
	X38 string `json:"x38"` // keyboard flags  (fixed)
	X39 int    `json:"x39"` // 0
	X42 string `json:"x42"` // "3.4.4" — flash/plugin version
	X43 string `json:"x43"` // canvas hash (fixed)
	X44 string `json:"x44"` // page-load timestamp (ms)
	X45 string `json:"x45"` // security cavity marker
	X46 string `json:"x46"` // "false"
	X48 string `json:"x48"` // ""
	X49 string `json:"x49"` // "{list:[],type:}"
	X50 string `json:"x50"` // ""
	X51 string `json:"x51"` // ""
	X52 string `json:"x52"` // ""
	X82 string `json:"x82"` // "_0x17a2|_0x1954"
}

const (
	canvasHash   = "742cc32c"
	b1FlagsX37   = "0|0|0|0|0|0|0|0|0|1|0|0|0|0|0|0|0|0|1|0|0|0|0|0"
	b1FlagsX38   = "0|0|1|0|1|0|0|0|0|0|1|0|1|0|1|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0"
	b1SecCavity  = "__SEC_CAV__1-1-1-1-1|__SEC_WSA__|"
	b1X82Markers = "_0x17a2|_0x1954"
)

func buildB1Payload(now time.Time) *b1Payload {
	return &b1Payload{
		X33: "0", X34: "0", X35: "0",
		X36: fmt.Sprintf("%d", randInt(1, 20)),
		X37: b1FlagsX37, X38: b1FlagsX38, X39: 0,
		X42: "3.4.4", X43: canvasHash, X44: fmt.Sprintf("%d", now.UnixMilli()),
		X45: b1SecCavity, X46: "false",
		X48: "", X49: "{list:[],type:}", X50: "", X51: "", X52: "",
		X82: b1X82Markers,
	}
}

func randInt(min, max int) int {
	return rand.Intn(max-min) + min
}

// marshalCompact serializes without escaping HTML metacharacters, matching
// Python json.dumps(..., ensure_ascii=False, separators=(",", ":")).
func marshalCompact(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}

// generateB1 produces the x-s-common b1 value for a cookie set.
func generateB1(_ map[string]string, _ string) (string, error) {
	payload := buildB1Payload(time.Now())
	jsonBytes, err := marshalCompact(payload)
	if err != nil {
		return "", fmt.Errorf("xhs: generate b1: %w", err)
	}
	ciphertext := rc4Encrypt([]byte(b1SecretKey), jsonBytes)

	// Replay urllib.parse.quote(ciphertext.decode("latin1"), safe="!*'()~_-")
	// then the %-segment reconstruction.
	quoted := urllibQuoteASCII(string(ciphertext))
	segments := strings.Split(quoted, "%")
	if len(segments) == 1 {
		return "", fmt.Errorf("xhs: generate b1: no encoded segments in quote output")
	}
	recon := make([]byte, 0, len(jsonBytes))
	for _, seg := range segments[1:] {
		if len(seg) < 2 {
			recon = append(recon, seg...)
			continue
		}
		b, err := hex.DecodeString(seg[:2])
		if err != nil {
			return "", fmt.Errorf("xhs: generate b1: bad %%segment: %w", err)
		}
		recon = append(recon, b[0])
		recon = append(recon, []byte(seg[2:])...)
	}

	enc := newBasicEncoders()
	return enc.encodeCustom(recon), nil
}

// urllibQuoteASCII replicates urllib.parse.quote(string, safe="!*'()~_-") for
// ASCII input: unreserved chars and the safe set are kept; everything else
// becomes %XX (uppercase hex).
func urllibQuoteASCII(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9',
			b == '-', b == '_', b == '.', b == '~', b == '!', b == '*',
			b == '\'', b == '(', b == ')':
			sb.WriteByte(b)
		default:
			sb.WriteByte('%')
			sb.WriteByte(hexDigits[b>>4])
			sb.WriteByte(hexDigits[b&0xF])
		}
	}
	return sb.String()
}
