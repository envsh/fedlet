package xhs

// Cross-language validation against the xhshow 0.2.0 Python reference.
//
// The signature below was produced by the reference implementation with
// fixed inputs:
//   GET /api/sns/web/v2/login/send_code
//   params: phone=13800138000, zone=86, type=login
//   a1 = 19ab1e5ce48c3b3c5c50e2c040a801cfcca0a4ac50000571046
//   timestamp = 1764896636.081
//
// The 144-byte payload carries a random seed/offsets, so byte equality is
// impossible; instead we verify that decodeXS/decodeX3 are exact inverses and
// that every deterministic field of the payload matches for both ports.

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// pyXSSig is the reference x-s for the fixed inputs above.
const pyXSSig = "XYS_2UQhPsHCH0c1PUhMHjIj2erjwjQhyoPTqBPT49pjHjIj2eHjwjQgynEDJ74AHjIj2ePjwjQTJdPIPAZlg94aGLTlGfY6nLl/n0mm2LTrzemk8eQTqdp9pLMawepnz/4x2bDA4FWUy0pr+FDF8oD62sRcyemh/AHILgSI+bpsySQy+9zyLAmjJBbHPMppzSY+/nR64/zd+jTo/UTz8MpyqD+p4g83PDbhzLTOPaRpPpmnPrkHaMDU8MYPznD7cSz+c9EIqMQCLDkcpnbLP9ls4DT/Jfznnfl0yLLIaSQQyAmOarEaLSz+qM8ncADEG/qE4bS+8LR64nMd+jRF/sHVHdWFH0ijJ9Qx8n+FHdF="

// pyXSCommon is the matching x-s-common from the same run (b1 is random, so we
// only check envelope shape against it).
const pyXSCommon = "2UQAPsHC+aIjqArjwjHjNsQhPsHCH0rjNsQhPaHCH0c1PUhMHjIj2eHjwjQgynEDJ74AHjIj2ePjwjQhyoPTqBPT49pjHjIj2ecj" +
	"wjHFN0W9N0ZjNsQh+aHCH0rEGnHl8/p08/chGA+jP9PMGALI8/Q0PecIG/WIPn+fG9+YPBrFGnPMPeZIPeL7P/ZF+jHVHdW9H0ij" +
	"HjIj2eqjwjHjNsQhwsHCHDDAwoQH8B4AyfRI8FS98g+Dpd4daLP3JFSb/BMsn0pSPM87nrldzSzQ2bPAGdb7zgQB8nph8emSy9E0" +
	"cgk+zSS1qgzianYt8LcE/LzN4gzaa/+NqMS6qS4HLozoqfQnPbZEp98QyaRSp9P98pSl4oSzcgmca/P78nTTL08z/sVManD9q9z1" +
	"J9p/8db8aob7JeQl4epsPrz6agW3Lr4ryaRApdz3agYDq7YM47HFqgzkanYMGLSbP9LA/bGIa/+nprSe+9LI4gzVPDbrJg+P4fpr" +
	"LFTALMm7+LSb4d+kpdzt/7b7wrQM498cqBzSpr8g/FSh+bzQygL9nSm7qSmM4epQ4flY/BQdqA+l4oYQ2BpAPp87arS34nMQyFSE" +
	"8nkdqMD6pMzd8/4SL7bF8aRr+7+rG7mkqBpD8pSUzozQcA8Szb87PDSb/d+/qgzVJfl/4LExpdzQ4fRSy7bFP9+y+7+nJAzdaLp/" +
	"2LSbJBL3cL8ra/+bLrTQwrQQyp4QnSm7cLS9z9iFq9pAnLSwq7Yn4M+QcA4S80D98/mfybmQyg8S+S4ULAYl4MpQz/4APnGIqA8g" +
	"cnpkpdz7qBkD8nkc4MYQ4SQ020Z98n8M4MQIcDRApM87wrSha/QQPAYkq7b7nf4n4rDF/bzxJFb98/8I8np8qg4hag8m8pPI/7Pl" +
	"zbkkanD7q9kjJ7PAGnMEagG9q9zl49bQc9ME8M8F2B4n4AzQz/4ApS8FzDSkcg+kqgz/aLpwq9zM4b+H4g4cJS8F8rShy9YQ4S8U" +
	"P04VyAQQ4fLl4gzeaLpr4rS3afp84gcFGSSa4Bh6PBpx8FD7anWFqAQM498tLo4/a/PMq9Tl4M4ozepA+S4mqA+Iyo4QyBRAP98O" +
	"qA+M4o+0Lo4YaL+tqM4c4ApQyLkSy9pl/rSea9px8sRA8SmFpLSh+7+h4g4r+rQ8GLSiLn4Q40pAPLF98/8d4d+k/BzS8b8FqFS3" +
	"npzQP9SVadpFqrShznlQzg8A2BQUPAYgO/FjNsQhwaHCN/rAwecAPArA+eqVHdWlPsHCPsIj2erlH0ijJfRUJnbVHdF="

func decodeShell(t *testing.T) *signer {
	t.Helper()
	return newSigner()
}

func decodePayloadFromXS(t *testing.T, s *signer, xs string) []byte {
	t.Helper()
	env, err := s.decodeXS(xs)
	if err != nil {
		t.Fatalf("decodeXS: %v", err)
	}
	x3, ok := env["x3"].(string)
	if !ok {
		t.Fatalf("decoded x-s has no x3 field: %v", env)
	}
	if !strings.HasPrefix(x3, x3Prefix) {
		t.Fatalf("x3 missing mns0301_ prefix: %q", x3)
	}
	dec, err := s.decodeX3(x3)
	if err != nil {
		t.Fatalf("decodeX3: %v", err)
	}
	if len(dec) != payloadLength {
		t.Fatalf("decoded payload len %d, want %d", len(dec), payloadLength)
	}
	return dec
}

const (
	pyTS        = 1764896636.081
	pyA1        = "19ab1e5ce48c3b3c5c50e2c040a801cfcca0a4ac50000571046"
	pyURI       = "/api/sns/web/v2/login/send_code"
	pyContent   = "/api/sns/web/v2/login/send_code?phone=13800138000&zone=86&type=login"
	pyDValueHex = "8c4f0b549c9406c031117653eb50516b" // md5(pyContent)
)

func checkPayloadInvariants(t *testing.T, payload []byte) {
	t.Helper()
	// version bytes
	if !strings.HasPrefix(string(payload[:4]), versionBytes) {
		t.Fatalf("payload version %x", payload[:4])
	}
	// seed (LE) and seed byte
	seed := uint32(payload[4]) | uint32(payload[5])<<8 | uint32(payload[6])<<16 | uint32(payload[7])<<24
	seedByte := byte(seed & 0xFF)

	// a1 field: length byte + 52-byte slot (right-padded with NULs)
	if payload[44] != a1Length {
		t.Fatalf("a1 length byte %d, want %d", payload[44], a1Length)
	}
	if got := strings.TrimRight(string(payload[45:97]), "\x00"); got != strings.TrimRight(pyA1, "\x00") {
		t.Fatalf("a1 payload mismatch: got %q", got)
	}

	// app id field
	if payload[97] != appIDLength {
		t.Fatalf("app id length byte %d", payload[97])
	}
	if string(payload[98:108]) != "xhs-pc-web" {
		t.Fatalf("app id payload mismatch: %q", payload[98:108])
	}

	// uri length
	urilen := uint32(payload[32]) | uint32(payload[33])<<8 | uint32(payload[34])<<16 | uint32(payload[35])<<24
	if int(urilen) != len(pyContent) {
		t.Fatalf("uri length %d, want %d", urilen, len(pyContent))
	}

	// md5 xor invariant: payload[36+i] == md5byte[i] ^ seedByte
	d, err := hex.DecodeString(pyDValueHex)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < md5XorLength; i++ {
		if payload[36+i] != d[i]^seedByte {
			t.Fatalf("md5 xor byte %d mismatch", i)
		}
	}

	// a3 prefix
	if string(payload[124:128]) != a3Prefix {
		t.Fatalf("a3 prefix %x", payload[124:128])
	}
}

func TestCrossPythonSignXS(t *testing.T) {
	s := newSigner()

	// 1) decode the Python reference signature and validate payload invariants.
	pyPayload := decodePayloadFromXS(t, s, pyXSSig)
	checkPayloadInvariants(t, pyPayload)

	// 2) produce our own signature with the same inputs and validate the same
	// invariants.
	params := []kvParam{
		{key: "phone", val: "13800138000"},
		{key: "zone", val: "86"},
		{key: "type", val: "login"},
	}
	goSig, err := s.signXS("GET", pyURI, pyA1, "xhs-pc-web", "", params, pyTS)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(goSig, xysPrefix) {
		t.Fatalf("go x-s prefix %q", goSig[:5])
	}
	goPayload := decodePayloadFromXS(t, s, goSig)
	checkPayloadInvariants(t, goPayload)

	// 3) envelope shape of the Python x-s-common (custom-base64 JSON).
	env, err := s.decodeXSCommon(pyXSCommon)
	if err != nil {
		t.Fatalf("decode x-s-common: %v", err)
	}
	if env["x5"] != pyA1 {
		t.Fatalf("x-s-common x5 %v, want a1", env["x5"])
	}
	if env["x0"] != "1" || env["x1"] != "4.3.5" || env["x3"] != "xhs-pc-web" || env["x4"] != "4.86.0" {
		t.Fatalf("x-s-common template mismatch: %v", env)
	}
}

// decodeXSCommon is a non-fingerprint helper used only by tests.
func (s *signer) decodeXSCommon(xs string) (map[string]any, error) {
	raw, err := s.enc.decodeCustom(xs)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return data, nil
}

// extractPayloadHex decodes the XYW_ envelope for format checks.
func extractPayloadHex(t *testing.T, s *signer, xyw string) string {
	t.Helper()
	if !strings.HasPrefix(xyw, xywPrefix) {
		t.Fatalf("xyw prefix missing: %q", xyw[:6])
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(xyw, xywPrefix))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		SignSvn string `json:"signSvn"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.SignSvn != "56" {
		t.Fatalf("xyw signSvn %q want 56", env.SignSvn)
	}
	return string(raw)
}

func TestSignXYWShape(t *testing.T) {
	s := newSigner()
	params := []kvParam{{key: "num", val: "30"}, {key: "cursor", val: ""}}
	sig, err := s.signXYW("GET", pyURI, pyA1, "xhs-pc-web", "", params, pyTS)
	if err != nil {
		t.Fatal(err)
	}
	_ = extractPayloadHex(t, s, sig) // asserts XYW_ prefix + signSvn==56
}
