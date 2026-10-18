package chargectl

import (
	"fmt"
	"strings"
)

// AppleSMC wire constants and the byte offsets of SMCKeyData_t (an 80-byte
// struct). The platform glue lives in smc_darwin.go; these pieces are pure Go so
// the encoding can be unit-tested on any host.
const (
	smcSelector       = 2 // KERNEL_INDEX_SMC
	smcCmdReadBytes   = 5
	smcCmdWriteBytes  = 6
	smcCmdReadKeyInfo = 9

	smcKeyDataSize = 80

	offKey         = 0  // UInt32 key
	offKeyInfoSize = 28 // keyInfo.dataSize
	offResult      = 40 // char result (SMC status byte)
	offData8       = 42 // char data8 (the command byte)
	offBytes       = 48 // UInt8 bytes[32]
)

// fourCC packs up to four ASCII bytes, left-aligned and NUL-padded, into the
// big-endian UInt32 an SMC key is transmitted as ("BCLM" -> 0x42434C4D).
func fourCC(s string) (uint32, bool) {
	if s == "" || len(s) > 4 {
		return 0, false
	}
	var v uint32
	for i := 0; i < 4; i++ {
		var c byte
		if i < len(s) {
			c = s[i]
		}
		v = v<<8 | uint32(c)
	}
	return v, true
}

// keyName renders a 4-byte SMC key back to its ASCII form.
func keyName(k uint32) string {
	b := []byte{byte(k >> 24), byte(k >> 16), byte(k >> 8), byte(k)}
	return strings.TrimRight(string(b), "\x00")
}

// putU32 stores v big-endian at off.
func putU32(b []byte, off int, v uint32) {
	b[off] = byte(v >> 24)
	b[off+1] = byte(v >> 16)
	b[off+2] = byte(v >> 8)
	b[off+3] = byte(v)
}

// getU32 reads a big-endian UInt32 at off.
func getU32(b []byte, off int) uint32 {
	return uint32(b[off])<<24 | uint32(b[off+1])<<16 | uint32(b[off+2])<<8 | uint32(b[off+3])
}

// hexBytes renders val as the lower-case hex the `smc` tool expects after -w.
func hexBytes(val []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(val)*2)
	for _, c := range val {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

// smcStopAllow returns the stop/allow payloads for a switch key.
func smcStopAllow(key string) (stop, allow []byte) {
	switch key {
	case "CH0B":
		return []byte{0x02}, []byte{0x00}
	case "CHTE":
		return []byte{0x01, 0x00, 0x00, 0x00}, []byte{0x00, 0x00, 0x00, 0x00}
	case "CHWA":
		return []byte{0x01}, []byte{0x00}
	case "CH0I", "CH0J", "CH0K":
		// Adapter cut: stop/discharge, allow.
		return []byte{0x01}, []byte{0x00}
	case "CHIE":
		// Tahoe adapter cut: 0x08 disables the adapter, 0x00 re-enables it.
		return []byte{0x08}, []byte{0x00}
	}
	return nil, nil
}

// smcResultName renders the SMC result byte (SMCKeyData_t.result) for logs.
func smcResultName(r byte) string {
	switch r {
	case 0:
		return "success"
	case 1:
		return "SMC error"
	case 132: // 0x84
		return "key not found"
	default:
		return fmt.Sprintf("SMC result 0x%x", r)
	}
}

// bfclValue mirrors bclm on Intel: BFCL is set 5 below the BCLM ceiling
// (clamped to 1..100) so the MagSafe LED / "fully charged" display matches. It
// is cosmetic and does not itself stop charging.
func bfclValue(level int) byte {
	v := level - 5
	if v < 1 {
		v = 1
	}
	if v > 100 {
		v = 100
	}
	return byte(v)
}

// le32 reads a little-endian uint32. The macOS 27 firmware limit keys (bfD0/
// bfE0) encode a ui32 percentage little-endian, unlike the rest of the SMC.
func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// le32b encodes v as a little-endian uint32.
func le32b(v uint32) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}
