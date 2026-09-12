package xhs

// Interactive risk-control handling for the xhs API.
//
// When the API answers 461/471/472 the response headers carry the captcha
// coordinates (verifytype / verifyuuid). Two interactive recovery paths are
// surfaced through the login UI (loginsrv.go):
//
//   - verifyType 124: the "secondary verification" QR route. Registering the
//     captcha returns a rid with which the web app builds the app-scan URL
//     https://www.xiaohongshu.com/web-login/qrcode-transfer?... (verified from
//     live traffic). The user scans it with the xiaohongshu app and then
//     clicks "continue" — the blocked request is retried with the refreshed
//     cookies.
//   - slider (verifyType 102, v2 redcaptcha): the register response carries a
//     DES-ECB encrypted captchaInfo (images + expected geometry). We decrypt
//     it (crypto/des, zero extra deps) and render the pictures in the login
//     page; after the user finishes the slide in-app the request is retried.
//     Programmatic auto-solving is intentionally out of scope (交互而非破解).
//
// relayRisk is the package-level riskResolver wired into xhsClient: it blocks
// the calling poll round until the user confirms completion or the timeout
// elapses, so a single request never hammers the API during verification.

import (
	"crypto/des"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// riskTimeout bounds how long a feed round waits for manual verification
// before giving up and logging the failure.
const riskTimeout = 5 * time.Minute

// riskChallenge is one blocked request's captcha coordinates.
type riskChallenge struct {
	status     int
	verifyType string
	verifyUUID string
}

// riskCaptchaData is the decrypted (/register captchaInfo) image payload.
type riskCaptchaData struct {
	CaptchaURL      string `json:"captchaUrl"`
	BackgroundURL   string `json:"backgroundUrl"`
	AnglePath       string `json:"anglePath"`
	TextAPI         string `json:"textApi"`
	Hint            string `json:"hint"`
	AngleConclusion string `json:"angleConclusion"`
}

// parseRiskChallenge reads the captcha coordinates from a blocked response.
func parseRiskChallenge(status int, h http.Header) riskChallenge {
	c := riskChallenge{status: status}
	if h != nil {
		c.verifyType = strings.TrimSpace(h.Get("verifytype"))
		c.verifyUUID = strings.TrimSpace(h.Get("verifyuuid"))
	}
	return c
}

func (c riskChallenge) recordLog() string {
	return fmt.Sprintf("type=%s uuid=%s", q(c.verifyType), q(c.verifyUUID))
}

func q(s string) string {
	if s == "" {
		return "?"
	}
	if len(s) > 20 {
		s = s[:20] + "…"
	}
	return s
}

// ---- pending risk state driven by the login UI ----

var (
	riskMu sync.Mutex
	// riskCur is the latest challenge waiting for manual attention.
	riskCur *riskChallenge
	// riskURL is the app-scan verification URL for riskCur (when known).
	riskURL string
	// riskImages holds the decrypted slider pictures for riskCur.
	riskImages *riskCaptchaData
	// riskDone is closed by the UI once the user confirms completion.
	riskDone chan struct{}
)

// pendingRisk returns the challenge/url/images currently needing manual
// verification (nil when none) plus a bool saying whether one is active.
func pendingRisk() (ch *riskChallenge, url string, imgs *riskCaptchaData, active bool) {
	riskMu.Lock()
	defer riskMu.Unlock()
	if riskCur == nil {
		return nil, "", nil, false
	}
	cp := *riskCur
	return &cp, riskURL, riskImages, true
}

// clearRisk marks the active risk as handled and unblocks the waiting round.
func clearRisk() {
	riskMu.Lock()
	c := riskCur
	riskCur = nil
	riskURL = ""
	riskImages = nil
	done := riskDone
	riskDone = nil
	riskMu.Unlock()
	if c != nil {
		logPrefix("risk cleared after manual verification")
	}
	if done != nil {
		close(done)
	}
}

// relayRisk is the riskResolver handed to xhsClient. It records the challenge,
// prepares the interactive verification material and blocks until the user
// confirms (via the login UI) or the timeout elapses.
func relayRisk(sig riskSignal, cookies map[string]string) (bool, map[string]string) {
	ch := parseRiskChallenge(sig.status, sig.headers)
	noteRisk(ch, cookies)

	// Network/decrypt work happens WITHOUT holding riskMu (captchaRegister
	// needs the lock itself); results are then snapshotted into the shared
	// pending state.
	url := secondaryVerifyURL(ch, cookies)

	riskMu.Lock()
	riskCur = &ch
	riskURL = url
	done := make(chan struct{})
	riskDone = done
	riskMu.Unlock()

	logPrefix("risk control %d blocked request (%s); manual verification required", sig.status, ch.recordLog())

	select {
	case <-done:
		return true, nil
	case <-time.After(riskTimeout):
		logPrefix("risk verification timed out")
		return false, nil
	}
}

// noteRisk logs the block and throttles subsequent requests.
func noteRisk(ch riskChallenge, cookies map[string]string) {
	noteRateLimit()
	body := make(map[string]any, 3)
	if ch.status != 0 {
		body["http"] = ch.status
	}
	if len(cookies) > 0 {
		body["has_session"] = cookies[xhsWebSessionName] != ""
	}
	pushError(fmt.Errorf("xhs: risk control %d (%s)", ch.status, ch.recordLog()))
}

// ---- app-scan (verifyType 124) ----

// captchaRegisterURL is the v2 captcha register endpoint. Payload verified
// against xhs issue #93 (ReaJason/xhs).
const captchaRegisterURL = "https://edith.xiaohongshu.com/api/redcaptcha/v2/captcha/register"

// secondaryVerifyURL tries to register the captcha and build the app-scan
// verification URL; it returns "" when the register call fails (the UI then
// shows the raw challenge information instead).
func secondaryVerifyURL(ch riskChallenge, cookies map[string]string) string {
	if ch.status == 0 || ch.verifyUUID == "" {
		return ""
	}
	rid, err := captchaRegister(ch)
	if err != nil {
		logPrefix("captcha register failed (%v); showing raw challenge", err)
		return ""
	}
	webid := cookies["webId"]
	u := secondaryVerifyURLFor(rid, ch, webid)
	logPrefix("secondary verification ready (web-login/qrcode-transfer)")
	return u
}

// secondaryVerifyURLFor builds the app-scan verification URL for a given rid.
// The URL pattern was verified from live traffic (urlscan), product-side init
// of the rid itself is 待实测.
func secondaryVerifyURLFor(rid string, ch riskChallenge, webid string) string {
	return fmt.Sprintf(
		"https://www.xiaohongshu.com/web-login/qrcode-transfer?rid=%s&verifyUuid=%s&verifyBiz=%d&verifyType=%s&webid=%s",
		rid, ch.verifyUUID, ch.status, ch.verifyType, webid,
	)
}

// captchaRegister obtains the rid for a challenge. The payload follows the
// web client (captchaVersion 2.0.0, secretId 000). 待实测: exact key values
// may drift with client updates.
func captchaRegister(ch riskChallenge) (string, error) {
	body := `{"captchaVersion":"2.0.0","secretId":"000","sourceSite":"",` +
		fmt.Sprintf(`"verifyBiz":%d,"verifyType":%q,"verifyUuid":%q}`, ch.status, ch.verifyType, ch.verifyUUID)
	payload := []byte(body)
	req, err := http.NewRequest(http.MethodPost, captchaRegisterURL, strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Origin", xhsBaseURL)
	req.Header.Set("Referer", xhsBaseURL+"/")
	req.Header.Set("User-Agent", xhsUA)
	// Sign the register request (a 2024 技述 article; the issue #93 sample
	// omits the headers — additive, and falls back to unsigned without a1).
	if a1 := client().cookies.get("a1"); a1 != "" {
		cookieRaw := client().cookieHeader()
		sh, err := buildSignedHeaders(signHeadersOptions{
			method:    http.MethodPost,
			uri:       captchaRegisterURL,
			a1:        a1,
			cookieRaw: cookieRaw,
			body:      body,
			ts:        float64AtNow(),
		})
		if err == nil {
			req.Header.Set("Cookie", cookieRaw)
			for _, kv := range sh.render() {
				k, v, _ := strings.Cut(kv, ": ")
				req.Header.Set(k, v)
			}
		}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, err := decodeJSON(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	data, ok := out["data"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("no data (%s)", truncate(stringOut(out), 120))
	}
	rid, _ := data["rid"].(string)
	if rid == "" {
		return "", fmt.Errorf("register returned no rid")
	}
	// Keep the encrypted payload so captcha.go can render the slide images.
	if info, _ := data["captchaInfo"].(string); info != "" {
		riskMu.Lock()
		riskImages = decryptCaptchaInfo(info)
		riskMu.Unlock()
	}
	return rid, nil
}

// ---- slider captcha images (verifyType 102) ----

// decryptCaptchaInfo decrypts the /register captchaInfo DES-ECB blob. The key
// is a fixed constant shipped in the web client JS ("76a2171c", from V.
// captchaInfo). 待实测: the key may rotate with client updates.
func decryptCaptchaInfo(enc string) *riskCaptchaData {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		logPrefix("captchaInfo base64 decode: %v", err)
		return nil
	}
	plain, err := desECBDecrypt(raw, []byte("76a2171c"))
	if err != nil {
		logPrefix("captchaInfo des decrypt: %v", err)
		return nil
	}
	var d riskCaptchaData
	if err := json.Unmarshal(plain, &d); err != nil {
		logPrefix("captchaInfo json parse: %v", err)
		return nil
	}
	return &d
}

// desECBDecrypt decrypts DES-ECB with PKCS7 unmarshalling (crypto/des only,
// zero added dependencies).
func desECBDecrypt(data, key []byte) ([]byte, error) {
	block, err := des.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data)%des.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext length %d not a block multiple", len(data))
	}
	out := make([]byte, len(data))
	for off := 0; off < len(data); off += des.BlockSize {
		block.Decrypt(out[off:off+des.BlockSize], data[off:off+des.BlockSize])
	}
	pad := int(out[len(out)-1])
	if pad < 1 || pad > des.BlockSize {
		return nil, fmt.Errorf("bad pkcs7 padding %d", pad)
	}
	return out[:len(out)-pad], nil
}
