package xhs

// Port of the xhshow 0.2.0 x-rap-param generator (core/xrap.py + utils/hash.py).
//
// Pipeline: TLV body structure -> XOR-obfuscate tail -> gzip -> cyclic-XOR with
// the AES session key -> block-cipher (SM4-variant) encrypt -> wrap in a header
// -> base64. Only used for homefeed/search-family endpoints (x-rap header).

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"time"
)

// --- block cipher constants (from xrap.py) ---

var xrapRoundKeys = [10][4]uint32{
	{0x6B714931, 0x44546377, 0x4B583930, 0x5A744179},
	{0x89314C98, 0xCD652FEF, 0x863D16DF, 0xDC4957A6},
	{0xC205330C, 0x0F601CE3, 0x895D0A3C, 0x55145D9A},
	{0xD205006E, 0xDD651C8D, 0x543816B1, 0x012C4B2B},
	{0x770C2B6F, 0xAA6937E2, 0xFE512153, 0xFF7D6A78},
	{0x7866FBF4, 0xD20FCC16, 0x2C5EED45, 0xD323873D},
	{0x90E9C67E, 0x42E60A68, 0x6EB8E72D, 0xBD9B6010},
	{0xE9C52BEE, 0xAB232186, 0xC59BC6AB, 0x7800A6BB},
	{0x13BA9A3E, 0xB899BBB8, 0x7D027D13, 0x0502DBA8},
	{0x50613270, 0xE8F889C8, 0x95FAF4DB, 0x90F82F73},
}

var xrapLastRoundKey = [4]uint32{0xF396B44F, 0x1B6E3D87, 0x8E94C95C, 0x1E6CE62F}

var xrapSBox = [256]byte{
	0x7A, 0x01, 0x58, 0xE0, 0x50, 0x4E, 0x02, 0x79, 0x1D, 0x4B, 0x53, 0xDA, 0x6B, 0x48, 0xD4, 0x52,
	0xED, 0x77, 0x12, 0x21, 0x14, 0x15, 0xEC, 0x10, 0x18, 0xE5, 0xB9, 0xF1, 0x0C, 0x08, 0xFC, 0x7D,
	0xF9, 0xCD, 0xB5, 0xC8, 0xE6, 0x37, 0x26, 0x87, 0x56, 0xBA, 0xB8, 0x2B, 0xAD, 0xF0, 0x68, 0xF7,
	0x8B, 0x8D, 0xD3, 0x5E, 0x36, 0x4D, 0x2E, 0x92, 0x31, 0x82, 0xF2, 0x29, 0x70, 0x3D, 0x2D, 0xD7,
	0xB6, 0x40, 0xB2, 0x43, 0x44, 0x80, 0x78, 0xD2, 0x0D, 0x49, 0x4A, 0x09, 0x63, 0x6C, 0x07, 0x3A,
	0x9E, 0xD5, 0x06, 0xC6, 0xE1, 0x62, 0xF4, 0x34, 0x24, 0x59, 0xA9, 0x57, 0x2A, 0x00, 0x3E, 0x17,
	0x2C, 0x0A, 0x1A, 0x42, 0xFA, 0x93, 0xBE, 0xDC, 0xF5, 0xB3, 0x6A, 0x13, 0xE8, 0x03, 0xC7, 0x97,
	0xBB, 0x73, 0x76, 0x86, 0xE3, 0x46, 0x72, 0x47, 0xD0, 0x05, 0x4C, 0x38, 0x7C, 0x1F, 0x81, 0xAB,
	0x75, 0x51, 0xEB, 0xF3, 0x32, 0x74, 0x11, 0x8F, 0x84, 0x89, 0x9C, 0x71, 0x22, 0x7E, 0x9D, 0xCF,
	0x3F, 0x91, 0x69, 0x65, 0x3C, 0x6D, 0x96, 0xA2, 0x98, 0x99, 0x33, 0x39, 0x9A, 0xCA, 0xC3, 0x9F,
	0xA0, 0xBC, 0xE4, 0xA3, 0xA4, 0x54, 0x7F, 0xA7, 0xA8, 0x04, 0x6F, 0x5D, 0xAC, 0xB7, 0x27, 0xAF,
	0xB0, 0x28, 0x41, 0xAE, 0xB4, 0x6E, 0x0B, 0x1B, 0xDF, 0x8E, 0x30, 0xB1, 0xFE, 0x90, 0x61, 0x60,
	0xC0, 0xCB, 0x5C, 0x0E, 0xEF, 0x16, 0x83, 0xEA, 0x20, 0xE9, 0xC9, 0x55, 0xC4, 0x45, 0x85, 0xCC,
	0x1E, 0xAA, 0x67, 0x8A, 0x7B, 0x35, 0xD6, 0x19, 0xD8, 0xD9, 0xC2, 0xDB, 0x94, 0xDD, 0x1C, 0xDE,
	0xA6, 0xFF, 0xF8, 0xBF, 0x5B, 0x5A, 0x0F, 0xE7, 0xC1, 0xBD, 0xD1, 0x66, 0xC5, 0x25, 0xEE, 0x8C,
	0xE2, 0x5F, 0x88, 0xA1, 0x3B, 0xA5, 0xF6, 0xCE, 0x95, 0x2F, 0x64, 0x23, 0xFB, 0xFD, 0x4F, 0x9B,
}

// xrapDefaultInteractionTrace / xrapDefaultEnvSnapshot mirror the Python
// defaults so body output can be reproduced byte-for-byte in tests.
var xrapDefaultInteractionTrace = mustHexBytes(
	"0002000200004700000000000001ffd9ffa8005c23fff1ffff001501ff90fff9000022fff2ffff" +
		"001501ffb800770061a5ffe3ffff002a01fffcffe70000f0ffe4ffff002a01ffd30000003e01" +
		"ffd40000003e01ffc8ffff005301ffbdfffa006901ffbefffb006901ffb1fff4007d01ffa6ff" +
		"ee009301ffa8ffef009301ff9effe900a801ff96ffe600be01ff97ffe600be01ff93ffe500d3" +
		"01ff92ffe500ea01ff92ffe4010501ff92ffe3011b01ff92ffe402d101ff93ffe502f201ff94" +
		"ffe6040501",
)

var xrapDefaultEnvSnapshot = mustHexBytes(
	"000000010000000000000000fffeffff00000000000000390001ffff00bc00000000006e0002" +
		"0001013400000000012f00000002011a00000000016100000000019f000000000247000000000279" +
		"0000000002b1",
)

// Derived LUT tables (SM4 T-box from the S-box over GF(2^8)).
var xrapLUT = buildXrapLUT()

func buildXrapLUT() [4][256]uint32 {
	gf2 := func(s byte) byte {
		x := uint16(s) << 1
		if x&0x100 != 0 {
			x ^= 0x11B
		}
		return byte(x & 0xFF)
	}
	gf3 := func(s byte) byte { return gf2(s) ^ s }
	var tab [4][256]uint32
	for i, s := range xrapSBox {
		a, d := uint32(gf2(s)), uint32(gf3(s))
		sv := uint32(s)
		tab[0][i] = (a << 24) | (sv << 16) | (sv << 8) | d
		tab[1][i] = (d << 24) | (a << 16) | (sv << 8) | sv
		tab[2][i] = (sv << 24) | (d << 16) | (a << 8) | sv
		tab[3][i] = (sv << 24) | (sv << 16) | (d << 8) | a
	}
	return tab
}

// xrapEncryptBlock16 encrypts one <=16-byte block (right zero-padded).
func xrapEncryptBlock16(block []byte) []byte {
	padded := make([]byte, 16)
	copy(padded, block)
	s0 := binary.BigEndian.Uint32(padded[0:4]) ^ xrapRoundKeys[0][0]
	s1 := binary.BigEndian.Uint32(padded[4:8]) ^ xrapRoundKeys[0][1]
	s2 := binary.BigEndian.Uint32(padded[8:12]) ^ xrapRoundKeys[0][2]
	s3 := binary.BigEndian.Uint32(padded[12:16]) ^ xrapRoundKeys[0][3]

	for r := 1; r < 10; r++ {
		rk := xrapRoundKeys[r]
		n0 := xrapLUT[0][(s0>>24)&0xFF] ^ xrapLUT[1][(s1>>16)&0xFF] ^ xrapLUT[2][(s2>>8)&0xFF] ^ xrapLUT[3][s3&0xFF] ^ rk[0]
		n1 := xrapLUT[0][(s1>>24)&0xFF] ^ xrapLUT[1][(s2>>16)&0xFF] ^ xrapLUT[2][(s3>>8)&0xFF] ^ xrapLUT[3][s0&0xFF] ^ rk[1]
		n2 := xrapLUT[0][(s2>>24)&0xFF] ^ xrapLUT[1][(s3>>16)&0xFF] ^ xrapLUT[2][(s0>>8)&0xFF] ^ xrapLUT[3][s1&0xFF] ^ rk[2]
		n3 := xrapLUT[0][(s3>>24)&0xFF] ^ xrapLUT[1][(s0>>16)&0xFF] ^ xrapLUT[2][(s1>>8)&0xFF] ^ xrapLUT[3][s2&0xFF] ^ rk[3]
		s0, s1, s2, s3 = n0, n1, n2, n3
	}

	var lastBytes [16]byte
	for i, w := range xrapLastRoundKey {
		binary.BigEndian.PutUint32(lastBytes[i*4:], w)
	}
	st := [4]uint32{s0, s1, s2, s3}
	out := make([]byte, 16)
	for row := 0; row < 4; row++ {
		for col := 0; col < 4; col++ {
			idx := (st[(row+col)&3] >> (24 - 8*col)) & 0xFF
			out[4*row+col] = xrapSBox[idx] ^ lastBytes[4*row+col]
		}
	}
	return out
}

func xrapEncryptBlocks(src []byte) []byte {
	out := make([]byte, 0, (len(src)+15)/16*16)
	for i := 0; i < len(src); i += 16 {
		end := i + 16
		if end > len(src) {
			end = len(src)
		}
		out = append(out, xrapEncryptBlock16(src[i:end])...)
	}
	return out
}

func xrapEncryptSessionKey(key []byte) []byte {
	enc := xrapEncryptBlock16(key[:16])
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], 16)
	return append(enc, l[:]...)
}

// --- body structure builder ---

func xrapFieldByte(tag uint16, val byte) []byte {
	out := make([]byte, 3)
	binary.BigEndian.PutUint16(out, tag)
	out[2] = val
	return out
}

func xrapFieldU32(tag uint16, val uint32) []byte {
	out := make([]byte, 6)
	binary.BigEndian.PutUint16(out, tag)
	binary.BigEndian.PutUint32(out[2:], val)
	return out
}

func xrapFieldU64(tag uint16, val uint64) []byte {
	out := make([]byte, 10)
	binary.BigEndian.PutUint16(out, tag)
	binary.BigEndian.PutUint64(out[2:], val)
	return out
}

func xrapFieldBlob(tag uint16, data []byte) []byte {
	out := make([]byte, 6+len(data))
	binary.BigEndian.PutUint16(out, tag)
	binary.BigEndian.PutUint32(out[2:], uint32(len(data)))
	copy(out[6:], data)
	return out
}

type xrapBodyInput struct {
	api              string
	body             string
	sessionKey       []byte // 16 bytes
	timestampMs      int64
	nonce            uint32
	obfuscationByte  byte
	interactionTrace []byte
	environmentSnap  []byte
}

func xrapBuildBodyStructure(in xrapBodyInput) []byte {
	hashInput := append([]byte(in.api), []byte(in.body)...)
	var buf bytes.Buffer

	writeU64 := func(tag uint16, v uint64) { buf.Write(xrapFieldU64(tag, v)) }
	writeU32 := func(tag uint16, v uint32) { buf.Write(xrapFieldU32(tag, v)) }
	writeByte := func(tag uint16, v byte) { buf.Write(xrapFieldByte(tag, v)) }
	writeBlob := func(tag uint16, d []byte) { buf.Write(xrapFieldBlob(tag, d)) }

	writeU64(0x03E8, uint64(in.timestampMs))
	writeU32(0x03E9, in.nonce)
	writeBlob(0x03EA, in.sessionKey)
	writeU32(0x03EB, xxh32(hashInput))

	for tg := uint16(1051); tg <= 1065; tg++ {
		writeByte(tg, 0)
	}
	writeByte(1070, 0)
	for tg := uint16(1066); tg <= 1069; tg++ {
		writeByte(tg, 0)
	}
	writeU32(1100, 0)
	for tg := uint16(1071); tg <= 1073; tg++ {
		writeByte(tg, 0)
	}

	writeU32(1075, 0x564)
	writeU32(1076, 0x2C)
	writeU64(1077, uint64(max64(0, in.timestampMs-0x434)))
	writeBlob(1078, in.interactionTrace)

	writeU32(1082, 0)
	writeU32(1084, 0)
	writeU32(1085, 0)
	writeU32(1086, 100)
	writeU64(1087, uint64(max64(0, in.timestampMs-0x2D7)))
	writeBlob(1088, in.environmentSnap)

	writeU32(1090, 0)
	writeU32(1097, 0)
	writeU32(1092, 0x566)
	writeU32(1094, 0x519)
	writeU64(1095, uint64(max64(0, in.timestampMs-0x218C)))
	writeU32(1093, 0)
	writeByte(1096, 0)
	writeBlob(1091, []byte{0x00, 0x00, 0xFF, 0xFF})
	for tg := uint16(1151); tg <= 1156; tg++ {
		writeByte(tg, 0)
	}

	b := buf.Bytes()
	tail := make([]byte, len(b)-16)
	for i, chunk := range b[16:] {
		tail[i] = chunk ^ in.obfuscationByte
	}
	out := make([]byte, 0, len(b))
	out = append(out, b[:16]...)
	out = append(out, tail...)
	return out
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func xrapCompressBody(raw []byte, mtime int64, level int) ([]byte, error) {
	var gzw bytes.Buffer
	gz, err := gzip.NewWriterLevel(&gzw, level)
	if err != nil {
		return nil, err
	}
	gz.Header.OS = 3
	gz.Header.ModTime = time.Unix(mtime, 0)
	if _, err := gz.Write(raw); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	out := gzw.Bytes()
	if len(out) >= 10 {
		out[9] = 0x03
	}
	return out, nil
}

func xrapCyclicXor(data, key []byte) []byte {
	out := make([]byte, len(data))
	for i, b := range data {
		out[i] = b ^ key[i%len(key)]
	}
	return out
}

// xrapSDKVersion matches Python XRAP_SDK_VERSION (default 10300).
const xrapSDKVersion uint32 = 10300

func xrapPackEnvelope(compressed, encKey []byte, salt string, encTime, sdkVersion uint32) string {
	xored := xrapCyclicXor(compressed, encKey)
	cipherBody := append(xrapEncryptBlocks(xored), u32beBytes(uint32(len(compressed)))...)
	content := append([]byte(salt), xrapEncryptSessionKey(encKey)...)
	content = append(content, cipherBody...)
	contentHash := xxh32(content)

	header := make([]byte, 0, 36)
	header = append(header, 0x07, 0x24, 0x01, byte(len(salt)))
	header = append(header, u32beBytes(1)...)
	header = append(header, u32beBytes(20)...)
	header = append(header, u32beBytes(uint32(len(cipherBody)))...)
	header = append(header, u32beBytes(contentHash)...)
	header = append(header, u32beBytes(sdkVersion)...)
	header = append(header, u32beBytes(encTime)...)
	header = append(header, make([]byte, 8)...)
	return base64.StdEncoding.EncodeToString(append(header, content...))
}

func u32beBytes(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}

// xrapParam is the caller for a single request; every optional value is
// randomized when nil/zero is passed (crypto/rand). Deterministic when
// overrideFns are supplied.
func xrapParam(opts xrapOptions) (string, error) {
	in := xrapBodyInput{
		api:             opts.api,
		body:            opts.body,
		timestampMs:     opts.timestampMs,
		nonce:           opts.nonce,
		obfuscationByte: opts.mask,
	}
	if opts.timestampMs == 0 {
		in.timestampMs = time.Now().UnixMilli()
	}
	if opts.nonce == 0 {
		n, err := randUint32()
		if err != nil {
			return "", err
		}
		in.nonce = n
	}
	if opts.mask == 0 {
		b, err := randByteRange(1, 256)
		if err != nil {
			return "", err
		}
		in.obfuscationByte = b
	}
	if len(opts.sessionKey) == 0 {
		k, err := randomAlnumBytes(16)
		if err != nil {
			return "", err
		}
		in.sessionKey = k
	} else {
		in.sessionKey = opts.sessionKey
	}
	in.interactionTrace = opts.tracePayload
	if len(in.interactionTrace) == 0 {
		in.interactionTrace = xrapDefaultInteractionTrace
	}
	in.environmentSnap = opts.envPayload
	if len(in.environmentSnap) == 0 {
		in.environmentSnap = xrapDefaultEnvSnapshot
	}

	raw := xrapBuildBodyStructure(in)

	mtime := opts.gzipMtime
	if mtime == 0 {
		mtime = time.Now().Unix()
	}
	compressed, err := xrapCompressBody(raw, mtime, 6)
	if err != nil {
		return "", err
	}

	encKey := opts.encKey
	if len(encKey) == 0 {
		k, err := randomAlnumBytes(16)
		if err != nil {
			return "", err
		}
		encKey = k
	}
	salt := opts.salt
	if salt == "" {
		n, err := xrapRandInt(4, 7)
		if err != nil {
			return "", err
		}
		b, err := randomAlnumBytes(n)
		if err != nil {
			return "", err
		}
		salt = string(b)
	}
	encTime := opts.encTime
	if encTime == 0 {
		n, err := xrapRandInt(60, 241)
		if err != nil {
			return "", err
		}
		encTime = uint32(n)
	}
	sdk := opts.sdkVersion
	if sdk == 0 {
		sdk = xrapSDKVersion
	}
	return xrapPackEnvelope(compressed, encKey, salt, encTime, sdk), nil
}

type xrapOptions struct {
	api          string
	body         string
	encKey       []byte
	salt         string
	sessionKey   []byte
	timestampMs  int64
	nonce        uint32
	mask         byte
	tracePayload []byte
	envPayload   []byte
	gzipMtime    int64
	encTime      uint32
	sdkVersion   uint32
}

// --- crypto/rand helpers ---

func mustHexBytes(s string) []byte {
	out, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return out
}

func randUint32() (uint32, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b[:]), nil
}

func randByteRange(min, max int) (byte, error) {
	n, err := xrapRandInt(min, max)
	if err != nil {
		return 0, err
	}
	return byte(n), nil
}

func xrapRandInt(min, max int) (int, error) {
	if max <= min {
		return min, nil
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return int(binary.BigEndian.Uint32(b[:])%uint32(max-min)) + min, nil
}

const xrapASCIIAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func randomAlnumBytes(n int) ([]byte, error) {
	out := make([]byte, n)
	mod := uint64(len(xrapASCIIAlphabet))
	var b [8]byte
	for i := 0; i < n; i++ {
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		v := binary.BigEndian.Uint64(b[:])
		out[i] = xrapASCIIAlphabet[v%mod]
	}
	return out, nil
}

// --- xxHash32 (port of xhshow utils/hash.py) ---

func xxhRotl(v uint32, shift uint) uint32 {
	return (v << shift) | (v >> (32 - shift))
}

func xxh32(data []byte, seed ...uint32) uint32 {
	var sd uint32
	if len(seed) > 0 {
		sd = seed[0]
	}
	const (
		p1 = uint32(0x9E3779B1)
		p2 = uint32(0x85EBCA77)
		p3 = uint32(0xC2B2AE3D)
		p4 = uint32(0x27D4EB2F)
		p5 = uint32(0x165667B1)
	)
	length := uint32(len(data))
	pos := 0
	var digest uint32

	if length >= 16 {
		acc1, acc2, acc3, acc4 := sd+p1+p2, sd+p2, sd, sd-p1
		end := length - 16
		for pos <= int(end) {
			for ref := 0; ref < 4; ref++ {
				word := binary.LittleEndian.Uint32(data[pos : pos+4])
				pos += 4
				switch ref {
				case 0:
					acc1 = xxhRotl(acc1+word*p2, 13) * p1
				case 1:
					acc2 = xxhRotl(acc2+word*p2, 13) * p1
				case 2:
					acc3 = xxhRotl(acc3+word*p2, 13) * p1
				default:
					acc4 = xxhRotl(acc4+word*p2, 13) * p1
				}
			}
		}
		digest = xxhRotl(acc1, 1) + xxhRotl(acc2, 7) + xxhRotl(acc3, 12) + xxhRotl(acc4, 18)
	} else {
		digest = sd + p5
	}
	digest += length
	for pos+4 <= len(data) {
		word := binary.LittleEndian.Uint32(data[pos : pos+4])
		pos += 4
		digest = xxhRotl(digest+word*p3, 17) * p4
	}
	for pos < len(data) {
		digest = xxhRotl(digest+uint32(data[pos])*p5, 11) * p1
		pos++
	}
	digest ^= digest >> 15
	digest *= p2
	digest ^= digest >> 13
	digest *= p3
	digest ^= digest >> 16
	return digest
}
