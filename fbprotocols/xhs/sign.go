package xhs

// Signing core, ported from the "xhshow" reference implementation
// (MIT licensed, pypi xhshow 0.2.0, 2026-03).
//
// Produces the headers the xhs web client needs:
//   - x-s:          XYS_ (default, non-data APIs) or XYW_ (data APIs that
//                   reject XYS_ with HTTP 406 since ~March 2026)
//   - x-s-common:   ARC4-encrypted browser fingerprint (b1) + JS-style CRC32
//   - x-t:          request timestamp (ms)
//   - x-b3-traceid / x-xray-traceid: trace ids
//   - x-rap-param:  opt-in, required by feed/search endpoints (see xrap.go)
//
// Signature algorithm notes:
//   - GET content = path + "?" + key=percent-encoded-value pairs (only
//     non-unreserved bytes escaped; "," kept)
//   - POST content = path + compact JSON. FIELD ORDER MATTERS: it is hashed
//     byte-exact, so bodies must use fixed-order structs, never maps.
//   - d_value = md5(content); m_value = md5(path) for POST.
//   - 144-byte payload (mns0301_), XORed against a 76-byte HEX_KEY for the
//     first 76 bytes, then base64-encoded with the X3 alphabet.
//
// 未实测: hot list / notify endpoints are target-tested; feed/search sign
// paths are ported 1:1 from xhshow and marked where unverified.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rc4"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"math/big"
	"math/rand"
	"net/url"
	"strings"
	"time"
)

const (
	stdBase64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	customBase64Alpha = "ZmserbBoHQtNP+wOcza/LpngG8yJq42KWYj0DSfdikx3VT16IlUAFM97hECvuRX5"
	x3Base64Alpha     = "MfgqrsbcyzPQRStuvC7mn501HIJBo2DEFTKdeNOwxWXYZap89+/A4UVLhijkl63G"

	// XORing key for the first 76 payload bytes (152 hex chars).
	hexKey = "71a302257793271ddd273bcee3e4b98d9d7935e1da33f5765e2ea8afb6dc77a51a499d23b67c20660025860cbf13d4540d92497f58686c574e508f46e1956344f39139bf4faf22a3eef120b79258145b2feb5193b6478669961298e79bedca646e1a693a926154a5a7a1bd1cf0dedb742f917a747a1e388b234f2277516db7116035439730fa61e9822a0eca7bff72d8"

	versionBytes   = "\x79\x68\x60\x29" // [121,104,96,41] — mns0301_ generation
	payloadLength  = 144
	a1Length       = 52
	appIDLength    = 10
	md5XorLength   = 8
	tsLELength     = 8
	a3Prefix       = "\x02\x61\x33\x10" // [2,97,51,16]
	x3Prefix       = "mns0301_"
	xysPrefix      = "XYS_"
	xywPrefix      = "XYW_"
	envOffsetMin   = 10
	envOffsetMax   = 50
	seqMin         = 15
	seqMax         = 50
	windowPropsMin = 1000
	windowPropsMax = 1200
	xywEnvFlags    = "0|0|0|1|0|0|1|0|0|0|1|0|0|0|0|1|0|0|1"
	xywAESKey      = "7cc4adla5ay0701v"
	xywAESIV       = "4uzjr7mbsibcaldp"

	b1SecretKey = "xhswebmplfbt"

	publicUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36 Edg/142.0.0.0"

	hexChars     = "abcdef0123456789"
	xraySeqMax   = 8388607 // 2^23-1
	xrayTsShift  = 23
	xrayPart2Len = 16
	b3TraceLen   = 16
)

var (
	hashIV      = [4]uint32{1831565813, 461845907, 2246822507, 3266489909}
	envTable    = [15]byte{115, 248, 83, 102, 103, 201, 181, 131, 99, 94, 4, 68, 250, 132, 21}
	envChecks   = [15]byte{0, 1, 18, 1, 0, 0, 0, 0, 0, 0, 3, 0, 0, 0, 0}
	hexKeyBytes []byte
)

func init() {
	h, err := hex.DecodeString(hexKey)
	if err != nil {
		panic("xhs: bad hexKey constant: " + err.Error())
	}
	hexKeyBytes = h
}

// kvParam is an ordered key/value pair used for GET params (and as a fixed
// field-order carrier for POST bodies via client.go).
type kvParam struct {
	key  string
	val  string
	list []string
}

// pValue returns the flat value, joining list params like the web client does.
func (p kvParam) pValue() string {
	if len(p.list) > 0 {
		return strings.Join(p.list, ",")
	}
	return p.val
}

// ---------------------------------------------------------------------------
// Custom base64 alphabets (translation over standard base64 output).

type basicEncoders struct {
	stdToCustom map[byte]byte
	customToStd map[byte]byte
	stdToX3     map[byte]byte
	x3ToStd     map[byte]byte
}

func newBasicEncoders() *basicEncoders {
	e := &basicEncoders{
		stdToCustom: make(map[byte]byte), customToStd: make(map[byte]byte),
		stdToX3: make(map[byte]byte), x3ToStd: make(map[byte]byte),
	}
	for i := 0; i < 64; i++ {
		s := stdBase64Alphabet[i]
		e.stdToCustom[s] = customBase64Alpha[i]
		e.customToStd[customBase64Alpha[i]] = s
		e.stdToX3[s] = x3Base64Alpha[i]
		e.x3ToStd[x3Base64Alpha[i]] = s
	}
	return e
}

func (e *basicEncoders) translate(data []byte, stdToAlt map[byte]byte) string {
	var sb strings.Builder
	for i := 0; i < len(data); i++ {
		if c, ok := stdToAlt[data[i]]; ok {
			sb.WriteByte(c)
		} else {
			sb.WriteByte(data[i]) // '=' padding stays
		}
	}
	return sb.String()
}

func (e *basicEncoders) untranslate(s string, altToStd map[byte]byte) ([]byte, error) {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if std, ok := altToStd[s[i]]; ok {
			sb.WriteByte(std)
		} else {
			sb.WriteByte(s[i])
		}
	}
	return base64.StdEncoding.DecodeString(sb.String())
}

func (e *basicEncoders) encodeCustom(data []byte) string {
	return e.translate([]byte(base64.StdEncoding.EncodeToString(data)), e.stdToCustom)
}
func (e *basicEncoders) encodeX3(data []byte) string {
	return e.translate([]byte(base64.StdEncoding.EncodeToString(data)), e.stdToX3)
}
func (e *basicEncoders) decodeCustom(s string) ([]byte, error) {
	return e.untranslate(s, e.customToStd)
}
func (e *basicEncoders) decodeX3(s string) ([]byte, error) { return e.untranslate(s, e.x3ToStd) }

// signer holds the per-request state used by the signing routines.
type signer struct {
	enc *basicEncoders
}

func newSigner() *signer { return &signer{enc: newBasicEncoders()} }

// ---------------------------------------------------------------------------
// JS-style CRC32 (the variant the xhs web client uses for x-s-common x9).
// Final value = (~c) ^ 0xEDB88320, matching JS `(-1 ^ c ^ 0xEDB88320) >>> 0`.

func crc32JSTable() []uint32 {
	tbl := make([]uint32, 256)
	for d := 0; d < 256; d++ {
		r := uint32(d)
		for i := 0; i < 8; i++ {
			if r&1 != 0 {
				r = (r >> 1) ^ 0xEDB88320
			} else {
				r >>= 1
			}
		}
		tbl[d] = r & 0xFFFFFFFF
	}
	return tbl
}

var jsCrcTable = crc32JSTable()

func crc32JSBytes(data []byte) int32 {
	c := uint32(0xFFFFFFFF)
	for _, b := range data {
		c = jsCrcTable[(c&0xFF)^uint32(b)] ^ (c >> 8)
	}
	return int32(0xFFFFFFFF ^ c ^ 0xEDB88320)
}

func crc32JSString(s string) int32 {
	// string_mode="js": charCodeAt & 0xFF — byte-identical to crc32 for ASCII.
	return crc32JSBytes([]byte(s))
}

// ---------------------------------------------------------------------------
// custom_hash_v2: 16-byte digest used for the a3 payload field. Input must be
// a multiple of 8 bytes (here: 8-byte LE timestamp + 16-byte md5(path)).

func rotl32(x, n uint32) uint32 {
	if n == 0 {
		return x
	}
	return (x<<n | x>>(32-n)) & 0xFFFFFFFF
}

func customHashV2(input []byte) []byte {
	if len(input)%8 != 0 {
		panic("xhs: custom_hash_v2 input must be a multiple of 8 bytes")
	}
	s0, s1, s2, s3 := hashIV[0], hashIV[1], hashIV[2], hashIV[3]
	length := uint32(len(input))

	s0 ^= length
	s1 ^= length << 8
	s2 ^= length << 16
	s3 ^= length << 24

	for i := 0; i+8 <= len(input); i += 8 {
		v0 := binary.LittleEndian.Uint32(input[i : i+4])
		v1 := binary.LittleEndian.Uint32(input[i+4 : i+8])

		s0 = rotl32((s0+v0)^s2, 7)
		s1 = rotl32((v0^s1)+s3, 11)
		s2 = rotl32((s2+v1)^s0, 13)
		s3 = rotl32((s3^v1)+s1, 17)
	}

	t0 := s0 ^ length
	t1 := s1 ^ t0
	t2 := s2 + t1
	t3 := s3 ^ t2

	s0 = (rotl32(t0, 9) + rotl32(t2, 17)) & 0xFFFFFFFF
	s1 = rotl32(t1, 13) ^ rotl32(t3, 19)
	s2 = (rotl32(t2, 17) + s0) & 0xFFFFFFFF
	s3 = rotl32(t3, 19) ^ s1

	out := make([]byte, 16)
	for i, s := range []uint32{s0, s1, s2, s3} {
		binary.LittleEndian.PutUint32(out[i*4:], s)
	}
	return out
}

// ---------------------------------------------------------------------------
// Payload construction.

func xorTransformArray(src []byte) []byte {
	out := make([]byte, len(src))
	for i, b := range src {
		if i < len(hexKeyBytes) {
			b ^= hexKeyBytes[i]
		}
		out[i] = b
	}
	return out
}

func le32(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }
func le64(v uint64) []byte { b := make([]byte, 8); binary.LittleEndian.PutUint64(b, v); return b }

type payloadParams struct {
	hexParameter  string // d_value: md5(content) lowercase hex (32)
	hexMD5Path    string // m_value: md5(path) for POST, else d_value
	a1Value       string
	appIdentifier string // xhs-pc-web
	stringParam   string // the signed content string
	timestamp     float64
}

func buildPayloadArray(p payloadParams) ([]byte, error) {
	seed := rand.Uint32()
	seedByte := byte(seed & 0xFF)

	payload := make([]byte, 0, payloadLength)
	payload = append(payload, versionBytes...)
	payload = append(payload, le32(seed)...)

	tsMS := int64(p.timestamp * 1000)
	tsBytes := le64(uint64(tsMS))
	payload = append(payload, tsBytes...)

	// Stateless env path (no session state): shift the effective timestamp by
	// a random offset like the reference library.
	offset := rand.Intn(envOffsetMax-envOffsetMin+1) + envOffsetMin
	effectiveTS := int64((p.timestamp - float64(offset)) * 1000)
	payload = append(payload, le64(uint64(effectiveTS))...)
	payload = append(payload, le32(uint32(rand.Intn(seqMax-seqMin+1)+seqMin))...)
	payload = append(payload, le32(uint32(rand.Intn(windowPropsMax-windowPropsMin+1)+windowPropsMin))...)
	payload = append(payload, le32(uint32(len([]byte(p.stringParam))))...)

	md5Bytes, err := hex.DecodeString(p.hexParameter)
	if err != nil {
		return nil, fmt.Errorf("xhs: sign payload: bad d_value hex: %w", err)
	}
	for i := 0; i < md5XorLength; i++ {
		payload = append(payload, md5Bytes[i]^seedByte)
	}

	a1Bytes := []byte(p.a1Value)
	if len(a1Bytes) > a1Length {
		a1Bytes = a1Bytes[:a1Length]
	}
	a1Padded := make([]byte, a1Length)
	copy(a1Padded, a1Bytes)
	payload = append(payload, byte(len(a1Padded)))
	payload = append(payload, a1Padded...)

	appBytes := []byte(p.appIdentifier)
	if len(appBytes) > appIDLength {
		appBytes = appBytes[:appIDLength]
	}
	appPadded := make([]byte, appIDLength)
	copy(appPadded, appBytes)
	payload = append(payload, byte(len(appPadded)))
	payload = append(payload, appPadded...)

	// part11: env-table check bytes (15 values XORed).
	payload = append(payload, 1, seedByte^envTable[0])
	for i := 1; i < 15; i++ {
		payload = append(payload, envTable[i]^envChecks[i])
	}

	md5PathBytes, err := hex.DecodeString(p.hexMD5Path)
	if err != nil {
		return nil, fmt.Errorf("xhs: sign payload: bad m_value hex: %w", err)
	}
	payload = append(payload, a3Prefix...)
	for _, b := range customHashV2(append(append([]byte{}, tsBytes...), md5PathBytes...)) {
		payload = append(payload, b^seedByte)
	}

	if len(payload) != payloadLength {
		return nil, fmt.Errorf("xhs: sign payload: built %d bytes, want %d", len(payload), payloadLength)
	}
	return payload, nil
}

// ---------------------------------------------------------------------------
// Content-string builders (byte-exact inputs to the md5 hashes).

// percentEncodeValue matches Python urllib.parse.quote(value, safe=","):
// every byte that is not unreserved (A-Za-z0-9 - _ . ~) and not "," is
// percent escaped; "=", "/" etc. are escaped here.
func percentEncodeValue(v string) string {
	const hexDigits = "0123456789ABCDEF"
	var sb strings.Builder
	for i := 0; i < len(v); i++ {
		b := v[i]
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9',
			b == '-', b == '_', b == '.', b == '~', b == ',':
			sb.WriteByte(b)
		default:
			sb.WriteByte('%')
			sb.WriteByte(hexDigits[b>>4])
			sb.WriteByte(hexDigits[b&0xF])
		}
	}
	return sb.String()
}

// buildContentString builds the signed content: path IDs + query, or path +
// body for POST. body is passed pre-serialized by client.go to keep field
// order stable.
func buildContentString(method, uri string, params []kvParam, body string) string {
	if method == "POST" {
		return uri + body
	}
	if len(params) == 0 {
		return uri
	}
	parts := make([]string, 0, len(params))
	for _, p := range params {
		parts = append(parts, p.key+"="+percentEncodeValue(p.pValue()))
	}
	return uri + "?" + strings.Join(parts, "&")
}

// ---------------------------------------------------------------------------
// Signature entry points.

func (s *signer) signXS(method, uri, a1 string, appID string, body string, params []kvParam, ts float64) (string, error) {
	uri = extractURI(uri)
	content := buildContentString(method, uri, params, body)
	dVal := md5hex(content)
	mVal := dVal
	if method == "POST" {
		mVal = md5hex(uri)
	}

	payload, err := buildPayloadArray(payloadParams{
		hexParameter:  dVal,
		hexMD5Path:    mVal,
		a1Value:       a1,
		appIdentifier: appID,
		stringParam:   content,
		timestamp:     ts,
	})
	if err != nil {
		return "", err
	}
	x3 := s.enc.encodeX3(xorTransformArray(payload)[:payloadLength])

	sig := struct {
		X0 string `json:"x0"`
		X1 string `json:"x1"`
		X2 string `json:"x2"`
		X3 string `json:"x3"`
		X4 string `json:"x4"`
	}{"4.3.5", "xhs-pc-web", "Windows", x3Prefix + x3, "object"}
	env, err := json.Marshal(sig)
	if err != nil {
		return "", err
	}
	return xysPrefix + s.enc.encodeCustom(env), nil
}

func (s *signer) signXYW(method, uri, a1 string, appID string, body string, params []kvParam, ts float64) (string, error) {
	uri = extractURI(uri)
	content := buildContentString(method, uri, params, body)
	tsMS := int64(ts * 1000)

	x1 := md5hex("url=" + content)
	message := fmt.Sprintf("x1=%s;x2=%s;x3=%s;x4=%d;", x1, xywEnvFlags, a1, tsMS)
	padded, err := pkcs7Pad([]byte(base64.StdEncoding.EncodeToString([]byte(message))), aes.BlockSize)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher([]byte(xywAESKey))
	if err != nil {
		return "", err
	}
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte(xywAESIV)).CryptBlocks(ciphertext, padded)
	payloadHex := hex.EncodeToString(ciphertext)

	xyw := struct {
		SignSvn     string `json:"signSvn"`
		SignType    string `json:"signType"`
		AppID       string `json:"appId"`
		SignVersion string `json:"signVersion"`
		Payload     string `json:"payload"`
	}{"56", "x2", appID, "1", payloadHex}
	env, err := json.Marshal(xyw)
	if err != nil {
		return "", err
	}
	return xywPrefix + base64.StdEncoding.EncodeToString(env), nil
}

func pkcs7Pad(in []byte, blockSize int) ([]byte, error) {
	if blockSize <= 0 || blockSize > 255 {
		return nil, fmt.Errorf("xhs: pkcs7: bad block size %d", blockSize)
	}
	padLen := blockSize - len(in)%blockSize
	pad := make([]byte, padLen)
	for i := range pad {
		pad[i] = byte(padLen)
	}
	return append(in, pad...), nil
}

// signXSCommon builds the x-s-common header (ARC4 b1 fingerprint + template).
func (s *signer) signXSCommon(cookies map[string]string) (string, error) {
	a1 := cookies["a1"]
	if a1 == "" {
		return "", fmt.Errorf("xhs: sign x-s-common: missing a1 cookie")
	}
	b1, err := generateB1(cookies, publicUserAgent)
	if err != nil {
		return "", err
	}
	x9 := crc32JSString(b1)

	sig := struct {
		S0  int    `json:"s0"`
		S1  string `json:"s1"`
		X0  string `json:"x0"`
		X1  string `json:"x1"`
		X2  string `json:"x2"`
		X3  string `json:"x3"`
		X4  string `json:"x4"`
		X5  string `json:"x5"`
		X6  string `json:"x6"`
		X7  string `json:"x7"`
		X8  string `json:"x8"`
		X9  int32  `json:"x9"`
		X10 int    `json:"x10"`
		X11 string `json:"x11"`
	}{
		S0: 5, S1: "",
		X0: "1", X1: "4.3.5", X2: "Windows", X3: "xhs-pc-web", X4: "4.86.0",
		X5: a1, X6: "", X7: "", X8: b1, X9: x9,
		X10: 0, X11: "normal",
	}
	env, err := json.Marshal(sig)
	if err != nil {
		return "", err
	}
	return s.enc.encodeCustom(env), nil
}

// decodeXS decrypts an XYS_ signature envelope back into its JSON fields —
// mirrors xhshow.decode_xs, used by self-check tests and diagnostics.
func (s *signer) decodeXS(xs string) (map[string]any, error) {
	xs = strings.TrimPrefix(xs, xysPrefix)
	raw, err := s.enc.decodeCustom(xs)
	if err != nil {
		return nil, fmt.Errorf("xhs: decode x-s: %w", err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("xhs: decode x-s: %w", err)
	}
	return data, nil
}

// decodeX3 decrypts the mns0301_ x3 field back to the pre-base64 byte array.
func (s *signer) decodeX3(x3 string) ([]byte, error) {
	x3 = strings.TrimPrefix(x3, x3Prefix)
	raw, err := s.enc.decodeX3(x3)
	if err != nil {
		return nil, fmt.Errorf("xhs: decode x3: %w", err)
	}
	return xorTransformArray(raw), nil
}

// ---------------------------------------------------------------------------
// ID & trace generation.

func generateA1() string {
	tsHex := fmt.Sprintf("%x", time.Now().UnixMilli())
	const a1Charset = "abcdefghijklmnopqrstuvwxyz1234567890"
	randomStr := make([]byte, 30)
	for i := range randomStr {
		randomStr[i] = a1Charset[rand.Intn(len(a1Charset))]
	}
	aPart := tsHex + string(randomStr) + "5" + "0" + "000"
	crcE := crc32.ChecksumIEEE([]byte(aPart))
	return (aPart + fmt.Sprintf("%d", crcE))[:52]
}

func webIDFromA1(a1 string) string {
	sum := md5.Sum([]byte(a1))
	return hex.EncodeToString(sum[:])
}

func xTimestamp(ts time.Time) string { return fmt.Sprintf("%d", ts.UnixMilli()) }

func b3TraceID() string {
	b := make([]byte, b3TraceLen)
	for i := range b {
		b[i] = hexChars[rand.Intn(len(hexChars))]
	}
	return string(b)
}

func xrayTraceID(ts time.Time) string {
	seq := rand.Intn(xraySeqMax + 1)
	part1 := fmt.Sprintf("%016x", (uint64(ts.UnixMilli())<<xrayTsShift)|uint64(seq))
	part2 := make([]byte, xrayPart2Len)
	for i := range part2 {
		part2[i] = hexChars[rand.Intn(len(hexChars))]
	}
	return part1 + string(part2)
}

func searchID() string {
	ts := uint64(time.Now().UnixMilli())
	randomPart := uint64(rand.Intn(2147483646)) + 1 // ceil(0x7FFFFFFE*random)
	n := new(big.Int).Lsh(new(big.Int).SetUint64(ts), 64)
	n.Add(n, new(big.Int).SetUint64(randomPart))
	return n.Text(36)
}

// ---------------------------------------------------------------------------
// xy-direction sharding key (murmur-style hash with int32 semantics).

const (
	shardC1 = 0xCC9E2D51
	shardC2 = 0x1B873593
)

func imul32(a, b uint32) int32 { return int32(a * b) }

func shardingKey(userID string) int32 {
	if userID == "" {
		return int32(rand.Intn(91) + 10)
	}
	data := []byte(userID)
	length := len(data)
	r := int32(151488)

	for o := 0; o+4 <= length; o += 4 {
		u := (uint32(data[o]) | uint32(data[o+1])<<8 | uint32(data[o+2])<<16 | uint32(data[o+3])<<24)
		u = uint32(imul32(u, shardC1))
		u = (u<<15 | u>>17) & 0xFFFFFFFF
		u = uint32(imul32(u, shardC2))
		r ^= int32(u)
		r = int32((uint32(r)<<13 | uint32(r)>>19) & 0xFFFFFFFF)
		ru := uint32(r)
		ru = (ru*5 + 0xE6546B64) & 0xFFFFFFFF
		r = int32(ru)
	}

	s := 4 * (length / 4)
	var c uint32
	rem := length % 4
	if rem >= 3 {
		c ^= uint32(data[s+2]) << 16
	}
	if rem >= 2 {
		c ^= uint32(data[s+1]) << 8
	}
	if rem >= 1 {
		c ^= uint32(data[s])
		c = uint32(imul32(c, shardC1))
		c = (c<<15 | c>>17) & 0xFFFFFFFF
		c = uint32(imul32(c, shardC2))
		r ^= int32(c)
	}

	r ^= int32(length)
	ru := uint32(r)
	ru ^= ru >> 16
	ru = uint32(imul32(ru, 0x85EBCA6B)) & 0xFFFFFFFF
	ru ^= ru >> 13
	ru = uint32(imul32(ru, 0xC2B2AE35)) & 0xFFFFFFFF
	ru ^= ru >> 16

	return int32(ru%100) + 1
}

// ---------------------------------------------------------------------------
// URL helpers.

func extractURI(urlIn string) string {
	u, err := url.Parse(urlIn)
	if err != nil || u.Path == "" || u.Path == "/" {
		return urlIn
	}
	return u.Path
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// rc4Encrypt runs ARC4 (RC4) over src using Go's stdlib crypto/rc4 — the same
// primitive the reference implementation needs, with zero new dependencies.
func rc4Encrypt(key, src []byte) []byte {
	c, err := rc4.NewCipher(key)
	if err != nil {
		panic("xhs: rc4: " + err.Error())
	}
	out := make([]byte, len(src))
	c.XORKeyStream(out, src)
	return out
}
