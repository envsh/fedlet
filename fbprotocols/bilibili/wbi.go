package bilibili

// WBI (web interface) request signing for api.bilibili.com endpoint parity.
//
// The flow (bilibili-API-collect docs/misc/sign/wbi.md): two 32-char keys are
// fetched from nav.wbi_img.img_url/sub_url (the URL basename without the .png
// extension), concatenated and reordered with mixinKeyEncTab (the browser
// mixin key), then every request adds wts (unix seconds) plus w_rid =
// md5(sort(encodeURIComponent(params)) + mixin_key). The signature only gates
// the /wbi/ endpoints (video recommend feeds); the hot board / dynamic feed /
// notifications are plain anonymous/logged-in calls and must NOT be signed with
// w_rid (they answer -403/-40352 style blocks if a stale w_rid leaks in).

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// mixinKeyEncTab is the 64-element reorder table from the web front-end
// (bilibili-API-collect). Reordering the img+sub key string by this table and
// truncating to 32 chars yields the WBI mixin key.
var mixinKeyEncTab = [64]int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49,
	33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13, 37, 48, 7, 16, 24, 55, 40,
	61, 26, 17, 0, 1, 60, 51, 30, 4, 22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11,
	36, 20, 34, 44, 52,
}

// wbiMixinFromKeys derives the mixin key (32 chars) from the two raw keys.
func wbiMixinFromKeys(imgKey, subKey string) string {
	raw := imgKey + subKey
	var b strings.Builder
	for i, idx := range mixinKeyEncTab {
		if idx < len(raw) {
			b.WriteByte(raw[idx])
		}
		if i == 31 {
			break
		}
	}
	return b.String()
}

// keyFromURL extracts the WBI key out of a wbi_img URL like
// https://i0.hdslb.com/bfs/wbi/6d6a9cfc9d6d9b8715a708d30e026390.png
func keyFromURL(u string) string {
	base := path.Base(strings.TrimSuffix(strings.TrimSpace(u), "/"))
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	return base
}

// wbiKeys are the current cached WBI keys (fetched from nav).
type wbiKeys struct {
	imgKey    string
	subKey    string
	mixinKey  string
	fetchedAt time.Time
}

var (
	wbiMu       sync.Mutex
	wbiKeysData wbiKeys
)

// wbiKeysFromNav extracts the two keys from the nav prize/wbi_img envelope.
func wbiKeysFromNav(nav map[string]any) {
	wbiImg, _ := nav["wbi_img"].(map[string]any)
	if wbiImg == nil {
		return
	}
	imgURL, _ := wbiImg["img_url"].(string)
	subURL, _ := wbiImg["sub_url"].(string)
	if imgURL == "" || subURL == "" {
		return
	}
	imgKey := keyFromURL(imgURL)
	subKey := keyFromURL(subURL)
	if len(imgKey) != 32 || len(subKey) != 32 {
		log.Printf("bilibili: wbi keys unexpected length img=%d sub=%d", len(imgKey), len(subKey))
		return
	}
	wbiMu.Lock()
	wbiKeysData = wbiKeys{
		imgKey:    imgKey,
		subKey:    subKey,
		mixinKey:  wbiMixinFromKeys(imgKey, subKey),
		fetchedAt: time.Now(),
	}
	wbiMu.Unlock()
}

// wbiSign signs a parameter map for a /wbi/ endpoint and returns the query
// string to append (sorted, encodeURIComponent-cased, wts + w_rid included).
// It needs the mixin key; if it has not been fetched yet 0-retry is assumed by
// the caller probing a fresh nav first.
func wbiSign(params map[string]string) (string, error) {
	wbiMu.Lock()
	mk := wbiKeysData.mixinKey
	wbiMu.Unlock()
	if mk == "" {
		return "", fmt.Errorf("bilibili: wbi mixin key not ready")
	}
	if params == nil {
		params = map[string]string{}
	}
	params["wts"] = fmt.Sprintf("%d", time.Now().Unix())
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, uriEncodeCaps(k)+"="+uriEncodeCaps(params[k]))
	}
	queryStr := strings.Join(pairs, "&")
	sum := md5.Sum([]byte(queryStr + mk))
	params["w_rid"] = hex.EncodeToString(sum[:])
	// Rebuild the sorted query with the added w_rid/wts included.
	keys2 := make([]string, 0, len(params))
	for k := range params {
		keys2 = append(keys2, k)
	}
	sort.Strings(keys2)
	pairs2 := make([]string, 0, len(keys2))
	for _, k := range keys2 {
		pairs2 = append(pairs2, uriEncodeCaps(k)+"="+uriEncodeCaps(params[k]))
	}
	return strings.Join(pairs2, "&"), nil
}

// uriEncodeCaps mirrors the browser's encodeURIComponent with upper-cased
// percent escapes (Go's url.QueryEscape uses lower-case and also escapes '~'
// differently — the server rejects a lowered/non-matching w_rid hash).
func uriEncodeCaps(s string) string {
	var hexUpper = "0123456789ABCDEF"
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == '~' {
			b.WriteRune(r)
			continue
		}
		for _, bb := range []byte(string(r)) {
			b.WriteByte('%')
			b.WriteByte(hexUpper[bb>>4])
			b.WriteByte(hexUpper[bb&0x0f])
		}
	}
	return b.String()
}

// buildWBIURL attaches a signed query string to a wbi endpoint URL.
func buildWBIURL(base string, params map[string]string) (string, error) {
	q, err := wbiSign(params)
	if err != nil {
		return "", err
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + q, nil
}

func decodeNavForWBI(body []byte) {
	var nav map[string]any
	if err := json.Unmarshal(body, &nav); err != nil {
		return
	}
	wbiKeysFromNav(nav)
}

// urlParams is a small helper: map[string]string from a net/url.Values.
func urlParams(v url.Values) map[string]string {
	m := make(map[string]string, len(v))
	for k := range v {
		m[k] = v.Get(k)
	}
	return m
}
