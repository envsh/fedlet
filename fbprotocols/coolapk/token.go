package coolapk

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"
)

// appHeaders builds the signed header bundle; the X-App-Token is computed fresh
// per call with a random device id (V3 weak sign, same scheme as RSSHub).
func appHeaders() map[string]string {
	return map[string]string{
		"User-Agent":       "Dalvik/2.1.0 (Linux; U; Android 10; Redmi K30 5G MIUI/V12.0.3.0.QGICMXM) (#Build; Redmi; Redmi K30 5G; QKQ1.191222.002 test-keys; 10) +CoolMarket/11.0-2101202",
		"X-Requested-With": "XMLHttpRequest",
		"X-App-Id":        "com.coolapk.market",
		"X-App-Token":     signToken(newDeviceID(), time.Now().Unix()),
		"X-Sdk-Int":       "29",
		"X-Sdk-Locale":    "zh-CN",
		"X-App-Version":   "11.0",
		"X-Api-Version":   "11",
		"X-App-Code":      "2101202",
	}
}

// signToken computes the V3 weak sign for a fixed device/now (testable golden).
func signToken(deviceID string, now int64) string {
	pre := "token://com.coolapk.market/c67ef5943784d09750dcfbb31020f0ab?" +
		md5Hex(fmt.Sprintf("%d", now)) + "$" + deviceID + "&com.coolapk.market"
	return md5Hex(base64.StdEncoding.EncodeToString([]byte(pre))) + deviceID + fmt.Sprintf("0x%x", now)
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newDeviceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}