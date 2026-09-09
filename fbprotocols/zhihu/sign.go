package zhihu

// zse-96 web API signature for www.zhihu.com/api/*.
//
// This is a direct Go port of zly2006/zhihu_sign_rs (src/lib.rs), itself based
// on work from zly2006/zhihu-plus-plus. Both are AGPL-3.0 licensed; the
// algorithm and lookup tables below are translated verbatim from that public
// implementation to keep behaviour identical.
//
// Header contract (2026-09):
//
//	x-zse-93 = "101_3_3.0"
//	x-zse-96 = "2.0_" + encrypt_x(md5(zse93 + "+" + pathname + "+" + d_c0 [+ "+" + body]))
//	x-requested-with = "fetch"
//
// Only d_c0 (a visitor cookie present for anonymous sessions too) feeds the
// signature, so signed calls work without a login.

import (
	"crypto/md5"
	"encoding/hex"
	"strings"
)

// zse93 is the fixed x-zse-93 version tag used by the current web signature.
const zse93 = "101_3_3.0"

// zk is the round-key schedule table of the block cipher (SM4 variant).
var zk = [32]uint32{
	1170614578, 1024848638, 1413669199, 3951632832, 3528873006, 2921909214, 4151847688, 3997739139,
	1933479194, 3323781115, 3888513386, 460404854, 3747539722, 2403641034, 2615871395, 2119585428,
	2265697227, 2035090028, 2773447226, 4289380121, 4217216195, 2200601443, 3051914490, 1579901135,
	1321810770, 456816404, 2903323407, 4065664991, 330002838, 3506006750, 363569021, 2347096187,
}

// zb is the S-box substitution table.
var zb = [256]byte{
	20, 223, 245, 7, 248, 2, 194, 209, 87, 6, 227, 253, 240, 128, 222, 91, 237, 9, 125, 157, 230,
	93, 252, 205, 90, 79, 144, 199, 159, 197, 186, 167, 39, 37, 156, 198, 38, 42, 43, 168, 217,
	153, 15, 103, 80, 189, 71, 191, 97, 84, 247, 95, 36, 69, 14, 35, 12, 171, 28, 114, 178, 148,
	86, 182, 32, 83, 158, 109, 22, 255, 94, 238, 151, 85, 77, 124, 254, 18, 4, 26, 123, 176, 232,
	193, 131, 172, 143, 142, 150, 30, 10, 146, 162, 62, 224, 218, 196, 229, 1, 192, 213, 27, 110,
	56, 231, 180, 138, 107, 242, 187, 54, 120, 19, 44, 117, 228, 215, 203, 53, 239, 251, 127, 81,
	11, 133, 96, 204, 132, 41, 115, 73, 55, 249, 147, 102, 48, 122, 145, 106, 118, 74, 190, 29, 16,
	174, 5, 177, 129, 63, 113, 99, 31, 161, 76, 246, 34, 211, 13, 60, 68, 207, 160, 65, 111, 82,
	165, 67, 169, 225, 57, 112, 244, 155, 51, 236, 200, 233, 58, 61, 47, 100, 137, 185, 64, 17, 70,
	234, 163, 219, 108, 170, 166, 59, 149, 52, 105, 24, 212, 78, 173, 45, 0, 116, 226, 119, 136,
	206, 135, 175, 195, 25, 92, 121, 208, 126, 139, 3, 75, 141, 21, 130, 98, 241, 40, 154, 66, 184,
	49, 181, 46, 243, 88, 101, 183, 8, 23, 72, 188, 104, 179, 210, 134, 250, 201, 164, 89, 216,
	202, 220, 50, 221, 152, 140, 33, 235, 214,
}

const signAlphabet = "6fpLRqJO8M/c3jnYxFkUVC4ZIG12SiH=5v0mXDazWBTsuw7QetbKdoPyAl+hN9rgE"

var key16 = [16]byte{'0', '5', '9', '0', '5', '3', 'f', '7', 'd', '1', '5', 'e', '0', '1', 'd', '7'}

// signZhihuRequest builds the three signing headers for url with the visitor
// cookie dC0 and an optional request body (POST/PUT).
func signZhihuRequest(u, dC0, body string) map[string]string {
	src := buildSignSource(u, dC0, body)
	sum := md5.Sum([]byte(src))
	zse96 := "2.0_" + encryptZseV4(hex.EncodeToString(sum[:]))
	return map[string]string{
		"x-zse-93":         zse93,
		"x-zse-96":         zse96,
		"x-requested-with": "fetch",
	}
}

func buildSignSource(u, dC0, body string) string {
	var b strings.Builder
	b.WriteString(zse93)
	b.WriteByte('+')
	b.WriteString(extractPathname(u))
	b.WriteByte('+')
	b.WriteString(dC0)
	if body != "" {
		b.WriteByte('+')
		b.WriteString(body)
	}
	return b.String()
}

// extractPathname returns the path part of a URL, e.g.
// "https://www.zhihu.com/api/v3/x?y=1" -> "/api/v3/x".
func extractPathname(u string) string {
	rest := u
	if idx := strings.Index(u, "//"); idx >= 0 {
		rest = u[idx+2:]
	}
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		return rest[idx:]
	}
	return "/" + rest
}

// encryptZseV4 is the final output-encoding cipher of the signature (seed byte
// + percent-coded input, block-cipher encrypted, then custom base64).
func encryptZseV4(input string) string {
	seed := byte(210)

	plain := make([]byte, 0, len(input)+18)
	plain = append(plain, seed, 0)
	plain = append(plain, encodeURIComponent(input)...)

	pad := 16 - len(plain)%16
	for k := 0; k < pad; k++ {
		plain = append(plain, byte(pad))
	}

	var first [16]byte
	for i := 0; i < 16; i++ {
		first[i] = plain[i] ^ key16[i] ^ 42
	}

	c0 := rBlock(first)
	cipher := append([]byte(nil), c0[:]...)
	if len(plain) > 16 {
		cipher = append(cipher, xBlocks(plain[16:], c0)...)
	}
	return customEncode(cipher)
}

func encodeURIComponent(s string) []byte {
	hexDigits := "0123456789ABCDEF"
	var out []byte
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch {
		case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9',
			b == '-', b == '_', b == '.', b == '!', b == '~', b == '*', b == '\'', b == '(', b == ')':
			out = append(out, b)
		default:
			out = append(out, '%', hexDigits[b>>4], hexDigits[b&0x0F])
		}
	}
	return out
}

func gTransform(tt uint32) uint32 {
	te := [4]byte{byte(tt >> 24), byte(tt >> 16), byte(tt >> 8), byte(tt)}
	tr := [4]byte{zb[te[0]], zb[te[1]], zb[te[2]], zb[te[3]]}
	ti := uint32(tr[0])<<24 | uint32(tr[1])<<16 | uint32(tr[2])<<8 | uint32(tr[3])
	rl := func(x uint32, n uint) uint32 { return x<<n | x>>(32-n) }
	return ti ^ rl(ti, 2) ^ rl(ti, 10) ^ rl(ti, 18) ^ rl(ti, 24)
}

func rBlock(in [16]byte) [16]byte {
	var tr [36]uint32
	tr[0] = be32(in[0:4])
	tr[1] = be32(in[4:8])
	tr[2] = be32(in[8:12])
	tr[3] = be32(in[12:16])
	for i := 0; i < 32; i++ {
		tr[i+4] = tr[i] ^ gTransform(tr[i+1]^tr[i+2]^tr[i+3]^zk[i])
	}
	var out [16]byte
	put32(tr[35], out[0:4])
	put32(tr[34], out[4:8])
	put32(tr[33], out[8:12])
	put32(tr[32], out[12:16])
	return out
}

// xBlocks encrypts data in a CBC-like chain using rBlock with the given IV.
// data must be a multiple of 16 bytes.
func xBlocks(data []byte, iv [16]byte) []byte {
	out := make([]byte, 0, len(data))
	for len(data) >= 16 {
		var mixed [16]byte
		for i := 0; i < 16; i++ {
			mixed[i] = data[i] ^ iv[i]
		}
		iv = rBlock(mixed)
		out = append(out, iv[:]...)
		data = data[16:]
	}
	return out
}

// customEncode is the non-standard base64-like final encoding (last block
// first, one byte xor-masked by 58 per 3-byte group).
func customEncode(b []byte) string {
	for len(b)%3 != 0 {
		b = append(b, 0)
	}
	var sb strings.Builder
	sb.Grow(len(b) / 3 * 4)
	i := 0
	for p := len(b) - 1; p >= 0; p -= 3 {
		var v uint32
		v |= uint32(b[p] ^ (58 >> (8 * (i % 4))))
		i++
		v |= uint32(b[p-1]^(58>>(8*(i%4)))) << 8
		i++
		v |= uint32(b[p-2]^(58>>(8*(i%4)))) << 16
		i++
		sb.WriteByte(signAlphabet[v&63])
		sb.WriteByte(signAlphabet[(v>>6)&63])
		sb.WriteByte(signAlphabet[(v>>12)&63])
		sb.WriteByte(signAlphabet[(v>>18)&63])
	}
	return sb.String()
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func put32(v uint32, b []byte) {
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
}
