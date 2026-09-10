package zhihu

// Request-body encryption for the zhihu Android account API.
//
// This is a direct Go port of zhihu-plus-plus
// (zly2006/zhihu-plus-plus, AGPL-3.0) ZhihuMessageBodyEncryptor: a custom
// SM4-derived cipher whose round constants (the PROTOCOL_DATA table below)
// were extracted from the official Android 11.3.0 client. The encryption only
// covers the phone-login forms (device guest, captcha, digits, sign-in); all
// other zhihu traffic is plaintext JSON.
//
// Pipeline (mirrors the Kotlin source byte for byte):
//   plain[i]      = swapPairs(form[i]) xor 0xbb, then PKCS-style padding where
//                   the pad byte is swapPairs(padding) xor 0xbb
//   CB-C mode:    block = block xor previous; previous = encryptBlock(block);
//                   output = swapPairs(previous)
//   prev[0..15]   = swapPairs(IV bytes), IV = "f0551856aa575faa"
//   encryptBlock  = 10 rounds over the extracted tables, final SWBO+S-BOX
//   output        = standard Base64

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// hmacSHA1Hex is the zhihu-plus-plus account signature: HMAC-SHA1 of the
// concatenated message under the given secret, lowercase hex. Verified against
// the official test vectors (guest-init and sign_in signatures).
func hmacSHA1Hex(secret, message string) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

const (
	encBlockSize = 16
	encRounds    = 10
	encPreMask   = 0xbb
	encIV        = "f0551856aa575faa"
)

// bodyEncryptor holds the decoded protocol tables (one allocation per login).
type bodyEncryptor struct {
	roundKeys []byte
	x0        []byte
	xr        []byte
	xf        []byte
	tables    [4][]byte
	sbox      []byte
}

func newBodyEncryptor() *bodyEncryptor {
	raw, err := base64.StdEncoding.DecodeString(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, encProtocolData))
	if err != nil {
		// The embedded table is fixed at build time; an invalid constant is a
		// programmer error, not a runtime condition.
		panic("zhihu: embedded protocol data is not valid base64: " + err.Error())
	}
	e := &bodyEncryptor{
		roundKeys: raw[0:176],
		x0:        raw[176:432],
		xr:        raw[432:688],
		xf:        raw[688:944],
		sbox:      raw[5040:5296],
	}
	e.tables[0] = raw[944:1968]
	e.tables[1] = raw[1968:2992]
	e.tables[2] = raw[2992:4016]
	e.tables[3] = raw[4016:5040]
	return e
}

// encryptForm encodes a URL-encoded form into the standard-base64 ciphertext
// the account API expects.
func (e *bodyEncryptor) encryptForm(form string) string {
	plain := []byte(form)
	padding := encBlockSize - len(plain)%encBlockSize
	input := make([]byte, len(plain)+padding)
	for i, v := range plain {
		input[i] = byte(swapPairs(v) ^ encPreMask)
	}
	pad := byte(swapPairs(byte(padding)) ^ encPreMask)
	for i := len(plain); i < len(input); i++ {
		input[i] = pad
	}

	iv := []byte(encIV)
	previous := make([]byte, encBlockSize)
	for i, b := range iv {
		previous[i] = byte(swapPairs(b))
	}

	encrypted := make([]byte, len(input))
	for off := 0; off < len(input); off += encBlockSize {
		block := make([]byte, encBlockSize)
		for i := 0; i < encBlockSize; i++ {
			block[i] = input[off+i] ^ previous[i]
		}
		previous = e.encryptBlock(block)
		for i, v := range previous {
			encrypted[off+i] = byte(swapPairs(v))
		}
	}
	return base64.StdEncoding.EncodeToString(encrypted)
}

// swapPairs moves each pair of adjacent bits one position (0x0F -> 0xFF style
// nibble shuffle, one byte at a time).
func swapPairs(v byte) byte {
	return ((v & 0x55) << 1) | ((v & 0xaa) >> 1)
}

// encodedXor combines two bytes through a 128-entry XOR-folding table.
func encodedXor(table []byte, left, right byte) byte {
	a := int(left) & 0xff
	b := int(right) & 0xff
	high := int(table[((a>>4)<<4)^(b>>4)]) & 0xf0
	low := int(table[((a&0xf)<<4)^(b&0xf)]) >> 4
	return byte(high ^ low)
}

// encryptBlock runs the 10-round table cipher over one 16-byte block.
func (e *bodyEncryptor) encryptBlock(block []byte) []byte {
	state := make([]byte, encBlockSize)
	for i := 0; i < encBlockSize; i++ {
		state[i] = encodedXor(e.x0, block[i], e.roundKeys[i])
	}

	positions := [4][4]int{
		{0, 4, 8, 12},
		{5, 9, 13, 1},
		{10, 14, 2, 6},
		{15, 3, 7, 11},
	}
	for round := 1; round < encRounds; round++ {
		mixed := make([]byte, encBlockSize)
		for tableIdx, srcPos := range positions {
			tbl := e.tables[tableIdx]
			for wordIdx, sourcePos := range srcPos {
				tableOffset := int(state[sourcePos]) * 4
				for byteIdx := 0; byteIdx < 4; byteIdx++ {
					outIdx := wordIdx*4 + byteIdx
					val := tbl[tableOffset+3-byteIdx]
					if tableIdx == 0 {
						mixed[outIdx] = val
					} else {
						mixed[outIdx] = encodedXor(e.xr, mixed[outIdx], val)
					}
				}
			}
		}
		keyOffset := round * encBlockSize
		state = make([]byte, encBlockSize)
		for i := 0; i < encBlockSize; i++ {
			state[i] = encodedXor(e.xr, mixed[i], e.roundKeys[keyOffset+i])
		}
	}

	shiftedPositions := [16]int{0, 5, 10, 15, 4, 9, 14, 3, 8, 13, 2, 7, 12, 1, 6, 11}
	keyOffset := encRounds * encBlockSize
	out := make([]byte, encBlockSize)
	for i := 0; i < encBlockSize; i++ {
		substituted := e.sbox[int(state[shiftedPositions[i]])]
		out[i] = encodedXor(e.xf, substituted, e.roundKeys[keyOffset+i])
	}
	return out
}

// encProtocolData embeds the official lookup tables (round keys, XOR tables
// and the 256-byte S-box). Whitespace is stripped before decoding.
const encProtocolData = `
jMG7yWvFZrgFKLB3cESv6Edbt7jX9WO7HZBsd4F/BfHuDEthz0/TYZp0QDiksPiHA23oZTjaz7LZUYQ991089fsOrTmIFXo/KiIRupwY6f5z0hIkHwGDoF11
fqZugPDYJgslEFGx1EuwclIWaRfHDuzdlvmp0D0JrldI1QArgRo1RXBkJmhhpziR1/3F8Shvh6UqaNMAxAGf7elAHHBK1O3ZCV3JHjeAEjwUy7ZjoeThxvzX
cWFdSKy5hZ8QNQ8r/qHnVbA83pfEdhJMhm4vD58+SRoprI74ZQhb4dTLun5Ca5eHAc0Q5z0t2/xQqXi4NJtvAoj2L6lGH3jGtuXWWGlFPSMc7wvGkIO+rH30
W9DO6aS8XUB6YfHfKD0Elh6BpvXLeN6TtzDqUQljKEyNGXleuaTlE8IF2/sxJWyPTZK623bJ/IarIFvgag02HpxAJ4EJZZrdPLUWQMFzrlj64wIYKjhFVGJz
jZqpucrX5vKLLB1EObKW1QRo5V39d6HEFAaBlm5yQlssOfnR6bnJo1d53/7CAu0etqeWiUElajzVsF/qqiD6hHjISxefCD5su9t2wfqHoyBS7WMAPhmVSdG9
UuOtLfqEds9FG5UCOWV6V7+m4BLFDd72Ni9kj0Obx+mvulBMf2Tw3yU8CpIWh/uv5V+8P9aex3wSTYBjIQqIKR1DML+Y3Alk51b0f6vFoPPCcdqQsj7pUwpl
LUmGHiuPDGuX1Tm0GUfMeaZS/+BYfdj/zQPrEbOnnYdPIGI16c3+3X5oWUepv4ScHD4CK21DPicY5QbHkIW8rXjwUtQKHy0+RVVrcoqeqLbF1Ob9OZJmBYL3
L6BOF3jCvOvUUBkHjZBkeU9cJT723OS7yameNUAWKaSA9WMCWunfxL9yT2aUjQLHFuoxLtvzXah1tAluEfyMU+g7dJXCKNJGs6ttCHWX6ziHUxb0rU+8JtLN
HnoO7pdM8Chpgtw1wFuntvyd5gJ5qxvEhWA/1S2yS1qL7JB9BNdisvAeS6hXyTAqUTJKpdAOu2gszp92gBro/uOC9hRiswvRlHYkwjOiW0k/VCzLsWLRC0uv
+xridIGUdRdohfYqlUkH4L9fqzTF1pP8g24cxnSi6A1YvkfaIjHJqtk3Q5Qu+L5ZCuYUiHloIkU63aN8zxFXsegD/WyeiNy1zSlWhjztqkob+w2XZXJMKF2w
zxOgdjXRgmSVDvvos96mQDniUIbLL3CcafMCGq/AtFUg/kmT3jhhhHbnHgHaFxcMNoqK8Bmamsjel5c/NU1NVziMjPRc1tZy+21tHcqHh0y1y8vFyY+PQvRo
aBxFa2vV6a+vUm35+RMEyMg8D7a2AUlvb9J8NjZidQ0NJ4fZ2ST5KiqYhu/vYSQCAn4Xk5PGLQkJczlKSlj2amoQ1JKSPseJiURm//8RMExMU0dpadS9ubmz
Q2Ji2mpXV5yA3t4pKjc3rH41NWrkcnL+1RsbBVTY2HzteXnztr+/sdyUlDguBwd/vLS0uHEBASsCwcE1XtXVeiIAAHeRcXFLm62tjXs9PW39Y2MZUtHRddcZ
GQSJ398izEREiCEwMK2YrKyEdwMDJh0TE7lV7e2n4aCgXVPo6K92OjpgMUFBWwzGxjL1LS2Xo/j437u7u7utU1PpOkVFXq/29tHEQkKO+GxsFP5lZRphUFCd
63t7+/JhYRVf5uahWerqqD2Dg/nvpKRQdDg4bIzk5GhNKSnjuc/PwjuNjf0TmJjPJzk5pAGxsQvITk6Gp/Pz1hYaGrARkZHLcjExZZdzc0YeFRW6wI6OSXAM
DCOq9fXe7HR0+NafnzFAbm7Zplpa4KRYWOwUGBi8lqqqgGlfX5J/BgYhBsrKMEYvL+Hofn72QiAg5/AsLJOxwMDN0pCQN4PS0iovNDSg/GZmEpKhoYVnWVmU
LAQEeM+EhEC3ycnESC4u5k4nJ+9KZ2fcA7i4Dyk/P6K4vr62jufnb4HQ0C3zKCifPIaG8j9GRlGP1NQgJTs7pRWdncfjoqJa5n9/8aX9/dfDgoJKrFZW4hgc
HLSzwsLKVtracOJwcPcrCwt7a/v7G5SoqIy/xMTACMzMNMJAQIfqp6dcYF5emfEhIZuK19cs9yMjloLg4GeI7u5meQoKKLSysr6rXV3t0B4eCW9UVJDNSUmD
B7OzBkFgYN3bm5s7XdPTeUwkJOiwzs7JofHx2x+WlsFLKyvrGpWVzj6Fhfpa5eWumnV1Tg3DwzndmZkzjenpY09kZNDnqalUhOLibtienjaysLC3UOzso7rH
x8zBgIBNYvDwFwm6ugiuVVXqID4+qQC8vANlW1uVGx0dvZl6ekhR4eGrxk9PgRwWFrIoDg52W93dfW739x+oXFzk0RAQDWj+/hacpqaCCrW1DpN4eE8mDw9x
naOjiaD8/NOiUVHlEhERtTdDQ1aL6+trnqWlin0zM2mF29sl7nd3/3g8PGRs9PQYDsXFOljc3HSp+vrYEJycw8tLS4s0iIj8vre3v98UFAD/JiaR5aurVTKB
gfULzc09RCIi7mTy8h7TEhIKzkdHj3MICC+QfHxDV+PjpmNSUpqVfX1Hn3Z2QeCurlnFi4tFIzIyqgW9vQfZHx8C+iUlnjNISF96BQUuFxcM2oqK8DaamsgZ
l5c/3k1NVzWMjPQ41tZyXG1tHfuHh0zKy8vFtY+PQsloaBz0a2vVRa+vUun5+RNtyMg8BLa2AQ9vb9JJNjZifA0NJ3XZ2SSHKiqY+e/vYYYCAn4kk5PGFwkJ
cy1KSlg5amoQ9pKSPtSJiUTH//8RZkxMUzBpadRHubmzvWJi2kNXV5xq3t4pgDc3rCo1NWp+cnL+5BsbBdXY2HxUeXnz7b+/sbaUlDjcBwd/LrS0uLwBAStx
wcE1AtXVel4AAHcicXFLka2tjZs9PW17Y2MZ/dHRdVIZGQTX398iiUREiMwwMK0hrKyEmAMDJncTE7kd7e2nVaCgXeHo6K9TOjpgdkFBWzHGxjIMLS2X9fj4
36O7u7u7U1PprUVFXjr29tGvQkKOxGxsFPhlZRr+UFCdYXt7++thYRXy5uahX+rqqFmDg/k9pKRQ7zg4bHTk5GiMKSnjTc/PwrmNjf07mJjPEzk5pCexsQsB
Tk6GyPPz1qcaGrAWkZHLETExZXJzc0aXFRW6Ho6OScAMDCNw9fXeqnR0+OyfnzHWbm7ZQFpa4KZYWOykGBi8FKqqgJZfX5JpBgYhf8rKMAYvL+FGfn726CAg
50IsLJPwwMDNsZCQN9LS0iqDNDSgL2ZmEvyhoYWSWVmUZwQEeCyEhEDPycnEty4u5kgnJ+9OZ2fcSri4DwM/P6Ipvr62uOfnb47Q0C2BKCif84aG8jxGRlE/
1NQgjzs7pSWdnccVoqJa439/8eb9/delgoJKw1ZW4qwcHLQYwsLKs9racFZwcPfiCwt7K/v7G2uoqIyUxMTAv8zMNAhAQIfCp6dc6l5emWAhIZvx19csiiMj
lvfg4GeC7u5miAoKKHmysr60XV3tqx4eCdBUVJBvSUmDzbOzBgdgYN1Bm5s729PTeV0kJOhMzs7JsPHx26GWlsEfKyvrS5WVzhqFhfo+5eWuWnV1TprDwzkN
mZkz3enpY41kZNBPqalU5+LiboSenjbYsLC3suzso1DHx8y6gIBNwfDwF2K6uggJVVXqrj4+qSC8vAMAW1uVZR0dvRt6ekiZ4eGrUU9PgcYWFrIcDg52KN3d
fVv39x9uXFzkqBAQDdH+/hZopqaCnLW1Dgp4eE+TDw9xJqOjiZ38/NOgUVHlohERtRJDQ1Y36+tri6Wlip4zM2l929slhXd3/+48PGR49PQYbMXFOg7c3HRY
+vrYqZycwxBLS4vLiIj8NLe3v74UFADfJiaR/6urVeWBgfUyzc09CyIi7kTy8h5kEhIK00dHj84ICC9zfHxDkOPjpldSUppjfX1HlXZ2QZ+urlngi4tFxTIy
qiO9vQcFHx8C2SUlnvpISF8zBQUuehcM2heK8DaKmsgZmpc/3pdNVzVNjPQ4jNZyXNZtHftth0zKh8vFtcuPQsmPaBz0aGvVRWuvUumv+RNt+cg8BMi2AQ+2
b9JJbzZifDYNJ3UN2SSH2SqY+SrvYYbvAn4kApPGF5MJcy0JSlg5SmoQ9mqSPtSSiUTHif8RZv9MUzBMadRHabmzvbli2kNiV5xqV94pgN43rCo3NWp+NXL+
5HIbBdUb2HxU2Hnz7Xm/sba/lDjclAd/Lge0uLy0AStxAcE1AsHVel7VAHciAHFLkXGtjZutPW17PWMZ/WPRdVLRGQTXGd8iid9EiMxEMK0hMKyEmKwDJncD
E7kdE+2nVe2gXeGg6K9T6DpgdjpBWzFBxjIMxi2X9S3436P4u7u7u1PprVNFXjpF9tGv9kKOxEJsFPhsZRr+ZVCdYVB7++t7YRXyYeahX+bqqFnqg/k9g6RQ
76Q4bHQ45GiM5CnjTSnPwrnPjf07jZjPE5g5pCc5sQsBsU6GyE7z1qfzGrAWGpHLEZExZXIxc0aXcxW6HhWOScCODCNwDPXeqvV0+Ox0nzHWn27ZQG5a4KZa
WOykWBi8FBiqgJaqX5JpXwYhfwbKMAbKL+FGL3726H4g50IgLJPwLMDNscCQN9KQ0iqD0jSgLzRmEvxmoYWSoVmUZ1kEeCwEhEDPhMnEt8ku5kguJ+9OJ2fc
Sme4DwO4P6IpP762uL7nb47n0C2B0Cif8yiG8jyGRlE/RtQgj9Q7pSU7nccVnaJa46J/8eZ//del/YJKw4JW4qxWHLQYHMLKs8LacFbacPficAt7Kwv7G2v7
qIyUqMTAv8TMNAjMQIfCQKdc6qdemWBeIZvxIdcsitcjlvcj4GeC4O5miO4KKHkKsr60sl3tq10eCdAeVJBvVEmDzUmzBgezYN1BYJs725vTeV3TJOhMJM7J
sM7x26HxlsEflivrSyuVzhqVhfo+heWuWuV1Tpp1wzkNw5kz3ZnpY43pZNBPZKlU56niboTinjbYnrC3srDso1Dsx8y6x4BNwYDwF2LwuggJulXqrlU+qSA+
vAMAvFuVZVsdvRsdekiZeuGrUeFPgcZPFrIcFg52KA7dfVvd9x9u91zkqFwQDdEQ/hZo/qaCnKa1Dgq1eE+TeA9xJg+jiZ2j/NOg/FHlolERtRIRQ1Y3Q+tr
i+ulip6lM2l9M9slhdt3/+53PGR4PPQYbPTFOg7F3HRY3PrYqfqcwxCcS4vLS4j8NIi3v763FADfFCaR/yarVeWrgfUygc09C80i7kQi8h5k8hIK0xJHj85H
CC9zCHxDkHzjplfjUppjUn1HlX12QZ92rlngrotFxYsyqiMyvQcFvR8C2R8lnvolSF8zSAUuegUM2hcX8DaKisgZmpo/3peXVzVNTfQ4jIxyXNbWHfttbUzK
h4fFtcvLQsmPjxz0aGjVRWtrUumvrxNt+fk8BMjIAQ+2ttJJb29ifDY2J3UNDSSH2dmY+SoqYYbv734kAgLGF5OTcy0JCVg5SkoQ9mpqPtSSkkTHiYkRZv//
UzBMTNRHaWmzvbm52kNiYpxqV1cpgN7erCo3N2p+NTX+5HJyBdUbG3xU2Njz7Xl5sba/vzjclJR/LgcHuLy0tCtxAQE1AsHBel7V1XciAABLkXFxjZutrW17
PT0Z/WNjdVLR0QTXGRkiid/fiMxERK0hMDCEmKysJncDA7kdExOnVe3tXeGgoK9T6Ohgdjo6WzFBQTIMxsaX9S0t36P4+Lu7u7vprVNTXjpFRdGv9vaOxEJC
FPhsbBr+ZWWdYVBQ++t7exXyYWGhX+bmqFnq6vk9g4NQ76SkbHQ4OGiM5OTjTSkpwrnPz/07jY3PE5iYpCc5OQsBsbGGyE5O1qfz87AWGhrLEZGRZXIxMUaX
c3O6HhUVScCOjiNwDAzeqvX1+Ox0dDHWn5/ZQG5u4KZaWuykWFi8FBgYgJaqqpJpX18hfwYGMAbKyuFGLy/26H5+50IgIJPwLCzNscDAN9KQkCqD0tKgLzQ0
EvxmZoWSoaGUZ1lZeCwEBEDPhITEt8nJ5kguLu9OJyfcSmdnDwO4uKIpPz+2uL6+b47n5y2B0NCf8ygo8jyGhlE/RkYgj9TUpSU7O8cVnZ1a46Ki8eZ/f9el
/f1Kw4KC4qxWVrQYHBzKs8LCcFba2vficHB7KwsLG2v7+4yUqKjAv8TENAjMzIfCQEBc6qenmWBeXpvxISEsitfXlvcjI2eC4OBmiO7uKHkKCr60srLtq11d
CdAeHpBvVFSDzUlJBgezs91BYGA725ubeV3T0+hMJCTJsM7O26Hx8cEflpbrSysrzhqVlfo+hYWuWuXlTpp1dTkNw8Mz3ZmZY43p6dBPZGRU56mpboTi4jbY
np63srCwo1Ds7My6x8dNwYCAF2Lw8AgJurrqrlVVqSA+PgMAvLyVZVtbvRsdHUiZenqrUeHhgcZPT7IcFhZ2KA4OfVvd3R9u9/fkqFxcDdEQEBZo/v6CnKam
Dgq1tU+TeHhxJg8PiZ2jo9Og/PzlolFRtRIREVY3Q0Nri+vrip6lpWl9MzMlhdvb/+53d2R4PDwYbPT0Og7FxXRY3NzYqfr6wxCcnIvLS0v8NIiIv763twDf
FBSR/yYmVeWrq/UygYE9C83N7kQiIh5k8vIK0xISj85HRy9zCAhDkHx8plfj45pjUlJHlX19QZ92dlngrq5FxYuLqiMyMgcFvb0C2R8fnvolJV8zSEguegUF
F4qal02M1m2Hy49oa6/5yLZvNg3ZKu8CkwlKapKJ/0xpuWJX3jc1chvYeb+UB7QBwdUAca09Y9EZ30QwrAMT7aDoOkHGLfi7U0X2QmxlUHth5uqDpDjkKc+N
mDmxTvMakTFzFY4M9XSfblpYGKpfBsovfiAswJDSNGahWQSEyS4nZ7g/vufQKIZG1Dudon/9glYcwtpwC/uoxMxAp14h1yPg7gqyXR5USbNgm9MkzvGWK5WF
5XXDmelkqeKesOzHgPC6VT68Wx164U8WDt33XBD+prV4D6P8URFD66Uz23c89MXc+pxLiLcUJquBzSLyEkcIfONSfXauizK9HyVIBQ==
`
