package xhs

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"io"
	"testing"
)

// Fixed inputs from tests/test_xrap.py so results are fully deterministic.
const (
	xrapTestAPI         = "//edith.xiaohongshu.com/api/sns/web/v1/homefeed"
	xrapTestBody        = `{"a":1}`
	xrapTestEncKey      = "wapilabkmyv4wl46"
	xrapTestSalt        = "mdzz94"
	xrapTestInnerKey    = "h9w3tl5em3w4t67c"
	xrapTestTimestamp   = int64(0x0000019EB07ACDB2)
	xrapTestNonce       = uint32(0xF95AD1C7)
	xrapTestMask        = byte(0x65)
	xrapTestGzipMtime   = int64(0x6A291532)
	xrapTestEncTime     = uint32(69)
	xrapTestHashInputXX = uint32(0xD2940151)
)

// xrapTestBodyHex is the byte-exact pre-compression TLV body produced by the
// Python reference for the fixed inputs above (XOR mask already applied).
var xrapTestBodyBytes = mustHexBytes(
	"03e80000019eb07acdb203e9f95ad1c7668f656565750d5c1256110950000856125111535206668eb7f16434617e6561" +
		"7965617865617b65617a65614565614465614765614665614165614065614365614265614d65614c65614b65614f6561" +
		"4e65614965614865612965656565614a656155656154656156656560016151656565496150656564fbd51fac1b615365" +
		"6565a165676567656522656565656565649abc9acd6539469a949a9a6570649af59a9c6565479a979a9a6570649add65" +
		"126504c09a869a9a654f649a999a826565959a819a9a654f649ab66565655b649ab16565655b649aad9a9a6536649ad8" +
		"9a9f650c649adb9a9e650c649ad49a916518649ac39a8b65f6649acd9a8a65f6649afb9a8c65cd649af39a8365db649a" +
		"f29a8365db649af69a8065b6649af79a80658f649af79a816460649af79a86647e649af79a8167b4649af69a80679764" +
		"9af19a83616064615f65656565615965656565615865656565615b65656501615a656564fbd51fafbe61256565653165" +
		"65656465656565656565659a9b9a9a656565656565655c65649a9a65d965656565650b65676564645165656565644a65" +
		"656567647f6565656564046565656564fa65656565672265656565671c6565656567d4612765656565612c6565656561" +
		"216565600361236565607c6122656564fbd51fc943612065656565612d6561266565656165659a9a611a6561e56561e4" +
		"6561e76561e66561e165",
)

func TestXrapEncryptBlock16KnownPair(t *testing.T) {
	plain, _ := hex.DecodeString("68ea78695e744b016d7a53a43a246167")
	got := xrapEncryptBlock16(plain)
	if hex.EncodeToString(got) != "d827df1c42d55ec61c0aec7d534fd817" {
		t.Fatalf("encrypt_block16 mismatch: %s", hex.EncodeToString(got))
	}
}

func TestXrapEncryptSessionKeyKnownPair(t *testing.T) {
	got := xrapEncryptSessionKey([]byte(xrapTestEncKey))
	if hex.EncodeToString(got) != "fac980a920308a95885597eb7b8b150a00000010" {
		t.Fatalf("session key mismatch: %s", hex.EncodeToString(got))
	}
}

func TestXrapXXH32(t *testing.T) {
	input := append([]byte(xrapTestAPI), []byte(xrapTestBody)...)
	if got := xxh32(input); got != xrapTestHashInputXX {
		t.Fatalf("xxh32 = %08x, want %08x", got, xrapTestHashInputXX)
	}
	// also matches the Python reference values
	if got := xxh32([]byte("abc")); got != 0x32D153FF {
		t.Fatalf("xxh32(abc) = %08x, want 32d153ff", got)
	}
	if got := xxh32([]byte{}); got != 0x02CC5D05 {
		t.Fatalf("xxh32() = %08x, want 02cc5d05", got)
	}
}

func TestXrapBuildBodyStructureGolden(t *testing.T) {
	got := xrapBuildBodyStructure(xrapBodyInput{
		api:              xrapTestAPI,
		body:             xrapTestBody,
		sessionKey:       []byte(xrapTestInnerKey),
		timestampMs:      xrapTestTimestamp,
		nonce:            xrapTestNonce,
		obfuscationByte:  xrapTestMask,
		interactionTrace: xrapDefaultInteractionTrace,
		environmentSnap:  xrapDefaultEnvSnapshot,
	})
	if string(got) != string(xrapTestBodyBytes) {
		t.Fatalf("body structure mismatch:\ngot  %x\nwant %x", got, xrapTestBodyBytes)
	}
}

func TestXrapFullStructureAndDeterminism(t *testing.T) {
	mk := func() string {
		out, err := xrapParam(xrapOptions{
			api:         xrapTestAPI,
			body:        xrapTestBody,
			encKey:      []byte(xrapTestEncKey),
			salt:        xrapTestSalt,
			sessionKey:  []byte(xrapTestInnerKey),
			timestampMs: xrapTestTimestamp,
			nonce:       xrapTestNonce,
			mask:        xrapTestMask,
			gzipMtime:   xrapTestGzipMtime,
			encTime:     xrapTestEncTime,
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	got := mk()
	if got != mk() {
		t.Fatal("x-rap not deterministic with fixed inputs")
	}

	raw, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw[:4]) != "\x07\x24\x01\x06" {
		t.Fatalf("header magic %x", raw[:4])
	}
	field := func(off int) uint32 { return binary.BigEndian.Uint32(raw[off : off+4]) }
	if field(4) != 1 || field(8) != 20 {
		t.Fatalf("header run ids: %d %d", field(4), field(8))
	}
	if field(20) != xrapSDKVersion {
		t.Fatalf("sdk version %d", field(20))
	}
	if field(24) != xrapTestEncTime {
		t.Fatalf("enc time %d", field(24))
	}
	if string(raw[36:42]) != xrapTestSalt {
		t.Fatalf("salt %q", raw[36:42])
	}
	// AES session key sits right after the salt, encrypted deterministically.
	encKey := xrapEncryptSessionKey([]byte(xrapTestEncKey))
	if string(raw[42:62]) != string(encKey) {
		t.Fatalf("encrypted session key mismatch")
	}
	// content_hash must equal xxh32 of the whole content tail.
	content := raw[36:]
	if field(16) != xxh32(content) {
		t.Fatalf("content hash self-check failed")
	}
	// cipher_body length field at 12 must match the tail framing.
	cipherLen := int(field(12))
	if len(raw) != 36+len(content) || len(content) != 6+20+cipherLen {
		t.Fatalf("framing mismatch: total %d content %d cipher %d", len(raw), len(content), cipherLen)
	}

	// gzip produced by our compressor must decompress back to the golden body
	// (mtime + OS byte are patched as the browser runtime does).
	gz, err := xrapCompressBody(xrapTestBodyBytes, xrapTestGzipMtime, 6)
	if err != nil {
		t.Fatal(err)
	}
	if gz[9] != 0x03 {
		t.Fatalf("gzip OS byte %02x, want 03", gz[9])
	}
	if len(gz) < 10 || gz[0] != 0x1f || gz[1] != 0x8b {
		t.Fatalf("gzip magic missing")
	}
	want := decompressGzip(t, gz)
	if string(want) != string(xrapTestBodyBytes) {
		t.Fatalf("gzip round-trip mismatch:\ngot  %x\nwant %x", want, xrapTestBodyBytes)
	}
}

func decompressGzip(t *testing.T, data []byte) []byte {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
