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
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"math/rand"
	"net/http"
	"strconv"
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

	// verifyType 102 (redcaptcha v2 rotation slider): attempt programmatic
	// solve first; on any failure the interactive path below stays available.
	if ch.verifyType == "102" && ch.status != 0 && ch.verifyUUID != "" {
		if newJar, ok := autoSolveSlider(ch, cookies); ok {
			return true, newJar
		}
		logPrefix("slider auto-solve failed; falling back to manual")
	}

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

// ---- programmatic redcaptcha v2 rotation-slider solving ----

// captchaCheckURL submits the solved rotation slider (known from the 2024
// "红书旋转滑块" protocol write-up). 待实测: endpoint path may drift.
const captchaCheckURL = "https://edith.xiaohongshu.com/api/redcaptcha/v2/captcha/check"

// desECBEncrypt is the DES-ECB PKCS7 encrypt half of desECBDecrypt; the web
// client encrypts the track/geometry fields of the check payload with it.
func desECBEncrypt(plain, key []byte) ([]byte, error) {
	block, err := des.NewCipher(key)
	if err != nil {
		return nil, err
	}
	pad := des.BlockSize - len(plain)%des.BlockSize
	if pad == 0 {
		pad = des.BlockSize
	}
	data := make([]byte, len(plain)+pad)
	copy(data, plain)
	for i := len(plain); i < len(data); i++ {
		data[i] = byte(pad)
	}
	for off := 0; off < len(data); off += des.BlockSize {
		block.Encrypt(data[off:off+des.BlockSize], data[off:off+des.BlockSize])
	}
	return data, nil
}

// generateTrack mirrors the web client's generate_Track(slideDistance): a
// JSON [[x,y,z],...] mouse-path for a slide of `distance` px.
func generateTrack(distance int) []byte {
	var sb strings.Builder
	sb.WriteByte('[')
	first := true
	for x := 0; x < distance; {
		x += 2
		y := -(x / 10)
		z := 2*(x-1) + rand.Intn(7) + 1
		if !first {
			sb.WriteByte(',')
		}
		first = false
		fmt.Fprintf(&sb, "[%d,%d,%d]", x, y, z)
	}
	sb.WriteByte(']')
	return []byte(sb.String())
}

// autoSolveSlider registers the challenge, solves the rotation angle and
// submits the check. Returns the refreshed cookie jar on verified success.
func autoSolveSlider(ch riskChallenge, cookies map[string]string) (map[string]string, bool) {
	rid, err := captchaRegister(ch)
	if err != nil {
		logPrefix("slider register failed: %v", err)
		return nil, false
	}
	riskMu.Lock()
	imgs := riskImages
	riskMu.Unlock()
	if imgs == nil {
		return nil, false
	}
	angle, width, ok := rotationAngle(imgs)
	if !ok {
		logPrefix("slider angle solve failed")
		return nil, false
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if jar, ok := submitSliderCheck(ch, rid, angle, width, attempt); ok {
			return jar, true
		}
		// checkCount increments per failed attempt; the server tolerates a few
		// before forcing a fresh challenge.
	}
	return nil, false
}

// rotationAngle returns the disc rotation (degrees) and the canvas width (px)
// that maps degrees → drag distance. The decrypted captchaInfo may already
// carry the expected conclusion; otherwise a gradient search is run.
func rotationAngle(imgs *riskCaptchaData) (float64, float64, bool) {
	if f, ok := parseAngleConclusion(imgs.AngleConclusion); ok {
		return f, captchaCanvasWidth(imgs), true
	}
	bg, err := fetchCaptchaImage(imgs.BackgroundURL)
	if err != nil {
		logPrefix("captcha bg fetch: %v", err)
		return 0, 0, false
	}
	q, err := fetchCaptchaImage(imgs.CaptchaURL)
	if err != nil {
		logPrefix("captcha query fetch: %v", err)
		return 0, 0, false
	}
	width := float64(bg.Bounds().Dx())
	angle, ok := searchRotationAngle(grayOf(bg), grayOf(q))
	return angle, width, ok
}

// parseAngleConclusion accepts a numeric angle over the confident range.
// 待实测: whether the web client leaks the answer here at all.
func parseAngleConclusion(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f <= 0 || f > 360 {
		return 0, false
	}
	return f, true
}

// captchaCanvasWidth is the slider canvas width used for the DES "width" field
// and the degrees→px mapping; defaults to 320 when the image can't be fetched.
func captchaCanvasWidth(imgs *riskCaptchaData) float64 {
	if bg, err := fetchCaptchaImage(imgs.BackgroundURL); err == nil {
		return float64(bg.Bounds().Dx())
	}
	return 320
}

func fetchCaptchaImage(u string) (image.Image, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", xhsUA)
	req.Header.Set("Referer", xhsBaseURL+"/")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return nil, err
	}
	return img, nil
}

func grayOf(im image.Image) *image.Gray {
	b := im.Bounds()
	g := image.NewGray(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			g.SetGray(x, y, color.GrayModel.Convert(im.At(x, y)).(color.Gray))
		}
	}
	return g
}

// searchRotationAngle rotates the query disc over the background and minimises
// the ring-vs-disc Sobel-gradient difference (a pure-Go, no-dependency port of
// the CV2 approach).
func searchRotationAngle(bg, q *image.Gray) (float64, bool) {
	qb := q.Bounds()
	r := min(qb.Dx(), qb.Dy()) / 2
	if r < 20 {
		return 0, false
	}
	disc := cropCircle(q, r)
	cx := bg.Bounds().Dx() / 2
	cy := bg.Bounds().Dy() / 2
	best, bestAng := math.Inf(1), 0.0
	// Scan coarse then fine around the best coarse bin to keep runtime bounded.
	for ang := 0.0; ang < 360; ang += 6 {
		d := mergedGradientDiff(bg, rotateNearest(disc, ang), cx, cy, r*7/9)
		if d < best {
			best, bestAng = d, ang
		}
	}
	for ang := bestAng - 5; ang <= bestAng+5; ang += 2 {
		a := math.Mod(ang+360, 360)
		d := mergedGradientDiff(bg, rotateNearest(disc, a), cx, cy, r*7/9)
		if d < best {
			best, bestAng = d, a
		}
	}
	return math.Mod(bestAng+360, 360), true
}

// cropCircle keeps the central disc of radius r (nearest-neighbour) in a new
// image with the same bounds, transparent being black here.
func cropCircle(g *image.Gray, r int) *image.Gray {
	b := g.Bounds()
	out := image.NewGray(g.Bounds())
	cx, cy := (b.Min.X+b.Max.X)/2, (b.Min.Y+b.Max.Y)/2
	r2 := r * r
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= r2 {
				out.SetGray(x, y, g.GrayAt(x, y))
			}
		}
	}
	return out
}

// rotateNearest rotates src by deg degrees around its centre (nearest sample).
func rotateNearest(src *image.Gray, deg float64) *image.Gray {
	b := src.Bounds()
	out := image.NewGray(b)
	cw, ch := float64(b.Dx()), float64(b.Dy())
	cos, sin := math.Cos(deg*math.Pi/180), math.Sin(deg*math.Pi/180)
	hx, hy := cw/2, ch/2
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			fx, fy := float64(x)-hx, float64(y)-hy
			sx := fx*cos + fy*sin + hx
			sy := -fx*sin + fy*cos + hy
			xi, yi := int(sx), int(sy)
			if xi < b.Min.X || xi >= b.Max.X || yi < b.Min.Y || yi >= b.Max.Y {
				continue
			}
			out.SetGray(x, y, src.GrayAt(xi, yi))
		}
	}
	return out
}

// mergedGradientDiff pastes disc over bg, then returns (outer-ring Sobel sum)
// − (inner-disc Sobel sum) on the merged canvas.
func mergedGradientDiff(bg, disc *image.Gray, cx, cy, r int) float64 {
	merged := image.NewGray(bg.Bounds())
	copy(merged.Pix, bg.Pix)
	db := disc.Bounds()
	for y := db.Min.Y; y < db.Max.Y; y++ {
		for x := db.Min.X; x < db.Max.X; x++ {
			px, py := cx-db.Dx()/2+(x-db.Min.X), cy-db.Dy()/2+(y-db.Min.Y)
			if px < merged.Bounds().Min.X || px >= merged.Bounds().Max.X ||
				py < merged.Bounds().Min.Y || py >= merged.Bounds().Max.Y {
				continue
			}
			if disc.GrayAt(x, y).Y != 0 {
				merged.SetGray(px, py, disc.GrayAt(x, y))
			}
		}
	}
	inner, outer := 0.0, 0.0
	m := merged.Bounds()
	rIn, rOut := r, r+30
	for y := m.Min.Y + 1; y < m.Max.Y-1; y++ {
		for x := m.Min.X + 1; x < m.Max.X-1; x++ {
			dx, dy := x-cx, y-cy
			d2 := dx*dx + dy*dy
			if d2 > rOut*rOut {
				continue
			}
			g := sobelMag(merged, x, y)
			if d2 <= rIn*rIn {
				inner += g
			} else {
				outer += g
			}
		}
	}
	return outer - inner
}

func sobelMag(g *image.Gray, x, y int) float64 {
	gx := float64(g.GrayAt(x+1, y-1).Y) + 2*float64(g.GrayAt(x+1, y).Y) + float64(g.GrayAt(x+1, y+1).Y) -
		float64(g.GrayAt(x-1, y-1).Y) - 2*float64(g.GrayAt(x-1, y).Y) - float64(g.GrayAt(x-1, y+1).Y)
	gy := float64(g.GrayAt(x-1, y+1).Y) + 2*float64(g.GrayAt(x, y+1).Y) + float64(g.GrayAt(x+1, y+1).Y) -
		float64(g.GrayAt(x-1, y-1).Y) - 2*float64(g.GrayAt(x, y-1).Y) - float64(g.GrayAt(x+1, y-1).Y)
	return math.Sqrt(gx*gx + gy*gy)
}

// submitSliderCheck posts the solved rotation to the check endpoint and takes
// any Set-Cookie/refresh jar from the response.
func submitSliderCheck(ch riskChallenge, rid string, angleDeg, width float64, attempt int) (map[string]string, bool) {
	distance := int(angleDeg / 360.0 * width)
	if distance < 10 {
		distance = 10
	}
	ms := 400 + rand.Intn(400)
	track, _ := desECBEncrypt(generateTrack(distance), []byte("PYrm8rMk"))
	mouseEnd, _ := desECBEncrypt([]byte(strconv.Itoa(distance)), []byte("WquqhEkd"))
	timeEn, _ := desECBEncrypt([]byte(strconv.Itoa(ms)), []byte("vPMvCY4K"))
	widthEn, _ := desECBEncrypt([]byte(strconv.Itoa(int(width))), []byte("WquqhEkd"))

	ci := struct {
		MouseEnd string `json:"mouseEnd"`
		Time     string `json:"time"`
		Track    string `json:"track"`
		Width    string `json:"width"`
	}{string(mouseEnd), string(timeEn), string(track), string(widthEn)}
	cib, err := json.Marshal(ci)
	if err != nil {
		return nil, false
	}
	payload := struct {
		RID            string `json:"rid"`
		VerifyType     string `json:"verifyType"`
		VerifyBiz      string `json:"verifyBiz"`
		VerifyUUID     string `json:"verifyUuid"`
		SourceSite     string `json:"sourceSite"`
		CaptchaVersion string `json:"captchaVersion"`
		CheckCount     string `json:"checkCount"`
		CaptchaInfo    string `json:"captchaInfo"`
	}{rid, "102", strconv.Itoa(ch.status), ch.verifyUUID, "", "2.0.0",
		strconv.Itoa(attempt), string(cib)}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, false
	}
	body := string(bodyBytes)

	req, err := http.NewRequest(http.MethodPost, captchaCheckURL, strings.NewReader(body))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Origin", xhsBaseURL)
	req.Header.Set("Referer", xhsBaseURL+"/")
	req.Header.Set("User-Agent", xhsUA)
	if a1 := client().cookies.get("a1"); a1 != "" {
		cookieRaw := client().cookieHeader()
		sh, err := buildSignedHeaders(signHeadersOptions{
			method:    http.MethodPost,
			uri:       captchaCheckURL,
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
		logPrefix("slider check request: %v", err)
		return nil, false
	}
	defer resp.Body.Close()
	out, err := decodeJSON(resp.Body)
	if err != nil {
		logPrefix("slider check decode: %v", err)
		return nil, false
	}
	code, _ := out["code"].(float64)
	var jar map[string]string
	for _, ck := range resp.Cookies() {
		if jar == nil {
			jar = map[string]string{}
		}
		jar[ck.Name] = ck.Value
	}
	if code == 0 && resp.StatusCode == http.StatusOK {
		// Any extra cookies the body carries (rare) are merged too.
		if data, ok := out["data"].(map[string]any); ok {
			for k, v := range data {
				if s, ok := v.(string); ok && len(jar) < 24 && (k == "rid" || strings.Contains(strings.ToLower(k), "cookie")) {
					jar[k] = s
				}
			}
		}
		return jar, true
	}
	logPrefix("slider check rejected: http %d code %v", resp.StatusCode, code)
	return nil, false
}
