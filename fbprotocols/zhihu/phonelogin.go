package zhihu

// zhihu phone + verify-code login over the Android account protocol.
//
// The 2026 web backend dropped the old send_verify_code / account/api/login
// endpoints (HTTP 404), so the phone route speaks api.zhihu.com the same way
// the first-party Android app does. This is a direct port of zhihu-plus-plus
// (zly2006/zhihu-plus-plus, AGPL-3.0) ZhihuPhoneLoginClient:
//
//  1. POST https://www.zhihu.com/udid mints a fresh web d_c0 (kept OUT of the
//     phone cookie jar until sign-in: the guest init answers 500 when d_c0 is
//     present). If no Set-Cookie d_c0 comes back, login stops early.
//  2. POST /api/account/prod/init/udid_guest exchanges an encrypted device
//     form (cloud-signatured: x-app-id 1355 + HMAC x-req-signature) for a
//     guest token + q_c0.
//  3. GET /captcha tests whether risk control demands a picture; when it does,
//     PUT /captcha returns the image and POST /captcha (encrypted input_text)
//     accepts the human answer.
//  4. POST /api/account/prod/auth/digits (encrypted username + client_id)
//     sends the SMS; errors 120001/120002 mean captcha, 120005 means re-check
//     the captcha state and retry once.
//  5. POST /api/account/prod/sign_in exchanges the SMS code for access_token +
//     a cookie map (z_c0/q_c0 + d_c0 rolled in from the web device step). The
//     cookies ARE the zhihu.com web session and are verified via /api/v4/me.
//
// The HTTP client is the shared `hc`, but every request carries the phone
// header set + the phone cookie jar only — never the shared web session.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	phoneAPIBase        = "https://api.zhihu.com"
	deviceGuestInitPath = "/api/account/prod/init/udid_guest"
	captchaPath         = "/captcha"
	authDigitsPath      = "/api/account/prod/auth/digits"
	signInPath          = "/api/account/prod/sign_in"
	webUdidURL          = "https://www.zhihu.com/udid"
	mobileClientID      = "8d5227e0aaaa4797a763ac64e0c3b8"
	mobileClientSecret  = "ecbefbf6b17e47ecb9035107866380"
	mobileSource        = "com.zhihu.android"
	digitsGrantType     = "digits"
	cloudAppID          = "1355"
	cloudAppSecret      = "dd49a835-56e7-4a0f-95b5-efd51ea5397f"
	cloudSignVersion    = "2"
	captchaNeededError  = 120005
	zhihuAndroidPhoneUA = "com.zhihu.android/Futureve/11.4.0 Mozilla/5.0 (Linux; Android 12; Android SDK built for arm64 " +
		"Build/SE1A.220621.001; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 " +
		"Chrome/57.0.1000.10 Mobile Safari/537.36"
)

// phoneLoginTestOverrides let tests pin the device/cookies/clock exactly like
// the zhihu-plus-plus MockEngine corpus (see zhihu_test.go).
var phoneLoginTestOverrides struct {
	mu      sync.Mutex
	cookies map[string]string
	device  *phoneLoginDeviceInfo
	nowFn   func() int64
}

// phoneLoginDeviceInfo mirrors the app's device-flavored form fields.
type phoneLoginDeviceInfo struct {
	timezoneOffsetSeconds int64
	appInstallTimeMillis  int64
	notificationEnabled   bool
	bluetoothAvailable    bool
	phoneBrand            string
	phoneModel            string
	androidRelease        string
	cpuType               string
	cpuCount              int
	cpuUsage              string
	totalMemoryMegabytes  int
	freeMemoryMegabytes   int
	totalStorageMegabytes int
	freeStorageMegabytes  int
}

// staticPhoneDeviceInfo fabricates one stable Android-emulator profile. The
// server that matters about most fields is a consistent "is this an app
// client?" shape; per-request freshness is the timestamp only.
func staticPhoneDeviceInfo() phoneLoginDeviceInfo {
	return phoneLoginDeviceInfo{
		timezoneOffsetSeconds: 28800,
		appInstallTimeMillis:  time.Now().UnixMilli(),
		notificationEnabled:   false,
		bluetoothAvailable:    true,
		phoneBrand:            "Android",
		phoneModel:            "Android SDK built for arm64",
		androidRelease:        "12",
		cpuType:               "aarch64",
		cpuCount:              4,
		cpuUsage:              "0.0",
		totalMemoryMegabytes:  256,
		freeMemoryMegabytes:   128,
		totalStorageMegabytes: 65536,
		freeStorageMegabytes:  32768,
	}
}

// zhihuPhoneToken is the sign_in result: the mobile access token plus the
// zhihu.com session cookies issued alongside.
type zhihuPhoneToken struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresAt    int64 // C.E. seconds
	Cookies      map[string]string
}

// zhihuPhoneDigitsOutcome reports how a digits request ended.
type zhihuPhoneDigitsOutcome struct {
	Sent     bool   // 短信已发出
	Captcha  bool   // 需要图形验证码
	ImageB64 string // data:image PNG/JPEG base64, when Captcha && image available
}

// zhihuPhoneClient drives one phone-login session. It is not safe for
// concurrent use; callers serialize with phoneClientMu.
type zhihuPhoneClient struct {
	cookies         map[string]string
	device          phoneLoginDeviceInfo
	enc             *bodyEncryptor
	authorization   string // "oauth 8d52..." then "bearer <guest-token>"
	deviceID        string
	webDeviceCookie string
	nowFn           func() int64
}

func newZhihuPhoneClient() *zhihuPhoneClient {
	phoneLoginTestOverrides.mu.Lock()
	defer phoneLoginTestOverrides.mu.Unlock()
	c := &zhihuPhoneClient{
		cookies:       map[string]string{},
		device:        staticPhoneDeviceInfo(),
		enc:           newBodyEncryptor(),
		authorization: "oauth " + mobileClientID,
	}
	if phoneLoginTestOverrides.cookies != nil {
		c.cookies = phoneLoginTestOverrides.cookies
	}
	if phoneLoginTestOverrides.device != nil {
		c.device = *phoneLoginTestOverrides.device
	}
	if phoneLoginTestOverrides.nowFn != nil {
		c.nowFn = phoneLoginTestOverrides.nowFn
	}
	// A pre-seeded d_c0 is the web device cookie of the session (mirrors the
	// Kotlin constructor): it jumps straight into webDeviceCookie and stays OUT
	// of the phone jar until sign-in.
	if d := c.cookies["d_c0"]; d != "" {
		delete(c.cookies, "d_c0")
		c.webDeviceCookie = d
	}
	return c
}

func (c *zhihuPhoneClient) nowEpoch() int64 {
	if c.nowFn != nil {
		return c.nowFn()
	}
	return time.Now().Unix()
}

// ---- top-level serialized entry points (used by the login UI) ----

var phoneClientMu sync.Mutex
var phoneClient *zhihuPhoneClient

// resetPhoneClient drops the in-flight phone-login session (fresh device on
// the next attempt). Tests call it between scenarios.
func resetPhoneClient() {
	phoneClientMu.Lock()
	phoneClient = nil
	phoneClientMu.Unlock()
}

func phoneClientLocked() *zhihuPhoneClient {
	if phoneClient == nil {
		phoneClient = newZhihuPhoneClient()
	}
	return phoneClient
}

// zhihuPhoneSendDigits runs the "send SMS code" step and reports whether the
// code was sent or a picture captcha must be solved first.
func zhihuPhoneSendDigits(phoneNumber string) (*zhihuPhoneDigitsOutcome, error) {
	phoneClientMu.Lock()
	defer phoneClientMu.Unlock()
	return phoneClientLocked().sendDigits(phoneNumber, true)
}

// zhihuPhoneVerifyCaptcha submits the picture-captcha answer and, when it
// validates, sends the SMS code straight away.
func zhihuPhoneVerifyCaptcha(phoneNumber, input string) (*zhihuPhoneDigitsOutcome, error) {
	if strings.TrimSpace(input) == "" {
		return nil, errors.New("图形验证码不能为空")
	}
	phoneClientMu.Lock()
	defer phoneClientMu.Unlock()
	c := phoneClientLocked()
	ok, err := c.verifyCaptcha(input)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("图形验证码不正确")
	}
	return c.sendDigits(phoneNumber, false)
}

// zhihuPhoneSignIn exchanges the SMS code for a mobile token whose cookie map
// is the zhihu.com web session.
func zhihuPhoneSignIn(phoneNumber, digits string) (*zhihuPhoneToken, error) {
	phoneClientMu.Lock()
	defer phoneClientMu.Unlock()
	token, err := phoneClientLocked().signIn(phoneNumber, digits)
	if err != nil {
		// A consumed session is not reusable: the next attempt must start a
		// fresh guest+device dance.
		resetPhoneClient()
		return nil, err
	}
	return token, nil
}

// ---- protocol steps ----

func (c *zhihuPhoneClient) sendDigits(phoneNumber string, recheckCaptcha bool) (*zhihuPhoneDigitsOutcome, error) {
	username, err := normalizePhoneNumber(phoneNumber)
	if err != nil {
		return nil, err
	}
	if err := c.ensureGuestToken(); err != nil {
		return nil, err
	}

	showCaptcha, _, err := c.requestCaptchaState()
	if err != nil {
		return nil, err
	}
	if showCaptcha {
		img, err := c.refreshCaptcha()
		if err != nil {
			return nil, err
		}
		return &zhihuPhoneDigitsOutcome{Captcha: true, ImageB64: img}, nil
	}

	return c.postDigits(username, recheckCaptcha)
}

func (c *zhihuPhoneClient) postDigits(username string, recheckCaptcha bool) (*zhihuPhoneDigitsOutcome, error) {
	form := formEncodePairs([][2]string{
		{"username", username},
		{"client_id", mobileClientID},
	})
	body, status, err := c.encryptedPost(phoneAPIBase+authDigitsPath, form)
	if err != nil {
		return nil, fmt.Errorf("发送短信验证码失败: %w", err)
	}
	if status == http.StatusOK {
		return &zhihuPhoneDigitsOutcome{Sent: true}, nil
	}

	code := extractZhihuErrorCode(body)
	if captchaInvalid(code) {
		img, err := c.refreshCaptcha()
		if err != nil {
			return nil, err
		}
		return &zhihuPhoneDigitsOutcome{Captcha: true, ImageB64: img}, nil
	}
	if code == captchaNeededError && recheckCaptcha {
		showCaptcha, _, err := c.requestCaptchaState()
		if err != nil {
			return nil, err
		}
		if showCaptcha {
			img, err := c.refreshCaptcha()
			if err != nil {
				return nil, err
			}
			return &zhihuPhoneDigitsOutcome{Captcha: true, ImageB64: img}, nil
		}
		return c.postDigits(username, false)
	}
	if msg := extractZhihuErrorMessage(body); msg != "" {
		return nil, fmt.Errorf("发送短信验证码失败: %s", msg)
	}
	return nil, fmt.Errorf("发送短信验证码失败(HTTP %d)", status)
}

func (c *zhihuPhoneClient) requestCaptchaState() (show bool, img string, err error) {
	req := c.newRequest(http.MethodGet, phoneAPIBase+captchaPath, nil)
	c.applyPhoneHeaders(req)
	body, status, err := c.do(req)
	if err != nil {
		return false, "", fmt.Errorf("检查图形验证码失败: %w", err)
	}
	if status != http.StatusOK {
		msg := extractZhihuErrorMessage(body)
		if msg != "" {
			return false, "", fmt.Errorf("检查图形验证码失败: %s", msg)
		}
		return false, "", fmt.Errorf("检查图形验证码失败(HTTP %d)", status)
	}
	var r struct {
		ShowCaptcha bool   `json:"show_captcha"`
		ImgBase64   string `json:"img_base64"`
		Cookie      string `json:"cookie"`
	}
	if json.Unmarshal(body, &r) == nil {
		c.ingestCaptchaTicketBody(r.Cookie)
		return r.ShowCaptcha, r.ImgBase64, nil
	}
	return false, "", nil
}

func (c *zhihuPhoneClient) refreshCaptcha() (string, error) {
	if err := c.ensureGuestToken(); err != nil {
		return "", err
	}
	req := c.newRequest(http.MethodPut, phoneAPIBase+captchaPath, nil)
	c.applyPhoneHeaders(req)
	body, status, err := c.do(req)
	if err != nil {
		return "", fmt.Errorf("获取图形验证码失败: %w", err)
	}
	if status != http.StatusOK {
		msg := extractZhihuErrorMessage(body)
		if msg != "" {
			return "", fmt.Errorf("获取图形验证码失败: %s", msg)
		}
		return "", fmt.Errorf("获取图形验证码失败(HTTP %d)", status)
	}
	var r struct {
		ImgBase64 string `json:"img_base64"`
	}
	if json.Unmarshal(body, &r) != nil {
		return "", errors.New("获取图形验证码失败: 响应解析错误")
	}
	return imageDataURI(r.ImgBase64), nil
}

// imageDataURI normalizes the captcha image to a data: URI the page can show
// directly.
func imageDataURI(b64 string) string {
	b64 = strings.TrimSpace(b64)
	if b64 == "" {
		return ""
	}
	if strings.HasPrefix(b64, "data:") {
		return b64
	}
	// It may be raw base64 image bytes; assume PNG body.
	return "data:image/png;base64," + b64
}

func (c *zhihuPhoneClient) verifyCaptcha(input string) (bool, error) {
	if err := c.ensureGuestToken(); err != nil {
		return false, err
	}
	form := formEncodePairs([][2]string{{"input_text", strings.TrimSpace(input)}})
	body, status, err := c.encryptedPost(phoneAPIBase+captchaPath, form)
	if err != nil {
		return false, fmt.Errorf("验证图形验证码失败: %w", err)
	}
	if status != http.StatusOK {
		if msg := extractZhihuErrorMessage(body); msg != "" {
			return false, fmt.Errorf("验证图形验证码失败: %s", msg)
		}
		return false, fmt.Errorf("验证图形验证码失败(HTTP %d)", status)
	}
	var r struct {
		Success bool `json:"success"`
	}
	if json.Unmarshal(body, &r) != nil {
		return false, errors.New("验证图形验证码失败: 响应解析错误")
	}
	return r.Success, nil
}

func (c *zhihuPhoneClient) signIn(phoneNumber, digits string) (*zhihuPhoneToken, error) {
	username, err := normalizePhoneNumber(phoneNumber)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(digits) == "" {
		return nil, errors.New("短信验证码不能为空")
	}
	if err := c.ensureGuestToken(); err != nil {
		return nil, err
	}

	timestamp := c.nowEpoch()
	signature := hmacSHA1Hex(mobileClientSecret,
		digitsGrantType+mobileClientID+mobileSource+strconv.FormatInt(timestamp, 10))
	form := formEncodePairs([][2]string{
		{"client_id", mobileClientID},
		{"digits", strings.TrimSpace(digits)},
		{"grant_type", digitsGrantType},
		{"signature", signature},
		{"source", mobileSource},
		{"timestamp", strconv.FormatInt(timestamp, 10)},
		{"username", username},
	})
	start := time.Now()
	body, status, err := c.encryptedPost(phoneAPIBase+signInPath, form)
	if err != nil {
		logf("zhihu: phone sign_in: transport error after %s: %v", time.Since(start).Round(time.Millisecond), err)
		return nil, fmt.Errorf("手机号登录失败: %w", err)
	}
	logf("zhihu: phone sign_in: http %d elapsed=%s body=%s", status, time.Since(start).Round(time.Millisecond), truncate(string(body), 200))
	if status != http.StatusOK {
		if msg := extractZhihuErrorMessage(body); msg != "" {
			return nil, fmt.Errorf("手机号登录失败: %s", msg)
		}
		return nil, fmt.Errorf("手机号登录失败(HTTP %d)", status)
	}
	var r struct {
		AccessToken  string            `json:"access_token"`
		RefreshToken string            `json:"refresh_token"`
		TokenType    string            `json:"token_type"`
		ExpiresIn    int64             `json:"expires_in"`
		Cookie       map[string]string `json:"cookie"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("手机号登录失败: 响应解析错误: %w", err)
	}
	if r.AccessToken == "" {
		return nil, errors.New("服务器未返回登录凭证")
	}
	for k, v := range r.Cookie {
		if strings.TrimSpace(v) != "" {
			c.cookies[k] = v
		}
	}
	if d := c.cookies["d_c0"]; strings.TrimSpace(d) == "" {
		if c.webDeviceCookie == "" {
			return nil, errors.New("服务器未返回必要的 Cookie d_c0")
		}
		c.cookies["d_c0"] = c.webDeviceCookie
	}
	return &zhihuPhoneToken{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		TokenType:    r.TokenType,
		ExpiresAt:    timestamp + r.ExpiresIn,
		Cookies:      mapClone(c.cookies),
	}, nil
}

func mapClone(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ensureGuestToken establishes the api.zhihu.com guest credential once:
// web d_c0 (kept separate), then the encrypted device-guest init. After it
// the Authorization is "bearer <guest-token>" and no longer re-enters.
func (c *zhihuPhoneClient) ensureGuestToken() error {
	if !strings.HasPrefix(c.authorization, "oauth ") {
		return nil
	}

	if c.webDeviceCookie == "" {
		if err := c.mintWebDeviceCookie(); err != nil {
			return err
		}
	}

	form := c.deviceForm()
	body, status, err := c.encryptedPost(phoneAPIBase+deviceGuestInitPath, form)
	if err != nil {
		return fmt.Errorf("初始化手机号登录失败: %w", err)
	}
	if status != http.StatusOK {
		if msg := extractZhihuErrorMessage(body); msg != "" {
			return fmt.Errorf("初始化手机号登录失败: %s", msg)
		}
		return fmt.Errorf("初始化手机号登录失败(HTTP %d)", status)
	}
	var r struct {
		Udid  string `json:"udid"`
		Guest struct {
			AccessToken string            `json:"access_token"`
			TokenType   string            `json:"token_type"`
			Cookie      map[string]string `json:"cookie"`
		} `json:"guest"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("初始化手机号登录失败: 响应解析错误: %w", err)
	}
	if r.Udid == "" {
		return errors.New("服务器未返回设备凭证")
	}
	if r.Guest.AccessToken == "" {
		return errors.New("服务器未返回访客凭证")
	}
	for k, v := range r.Guest.Cookie {
		if strings.TrimSpace(v) != "" {
			c.cookies[k] = v
		}
	}
	c.deviceID = r.Udid
	tt := r.Guest.TokenType
	if tt == "" {
		tt = "bearer"
	}
	c.authorization = tt + " " + r.Guest.AccessToken
	return nil
}

// mintWebDeviceCookie fetches www.zhihu.com/udid for a fresh d_c0 while
// keeping the phone cookie jar free of web cookies (the guest init turns that
// into a 500). The jar object is retained (only its contents are replaced) so
// callers keep observing the same map. Returns 500-free d_c0 only when the
// server actually set one.
func (c *zhihuPhoneClient) mintWebDeviceCookie() error {
	saved := mapClone(c.cookies)

	req := c.newRequest(http.MethodPost, webUdidURL, nil)
	c.applyPhoneHeaders(req)
	resp, err := hc.Do(req)
	// The phone jar must never receive web preheating cookies before sign-in.
	for k := range c.cookies {
		delete(c.cookies, k)
	}
	for k, v := range saved {
		c.cookies[k] = v
	}
	if err != nil {
		return fmt.Errorf("初始化网页设备凭证失败: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("初始化网页设备凭证失败(HTTP %d)", resp.StatusCode)
	}
	for _, sc := range resp.Header.Values("Set-Cookie") {
		name, value, _ := strings.Cut(sc, "=")
		value, _, _ = strings.Cut(value, ";")
		if name == "d_c0" && value != "" {
			c.webDeviceCookie = value
		}
	}
	if c.webDeviceCookie == "" {
		return errors.New("服务器未返回必要的 Cookie d_c0")
	}
	return nil
}

// deviceForm builds the guest-init form with the exact key order and
// percent-encoding the Android client emits (see the corpus in the tests).
func (c *zhihuPhoneClient) deviceForm() string {
	return formEncodePairs([][2]string{
		{"app_build", "40408"},
		{"app_install_time", strconv.FormatInt(c.device.appInstallTimeMillis, 10)},
		{"app_ticket", "interface is empty"},
		{"app_version", "11.4.0"},
		{"bt_ck", bool01(c.device.bluetoothAvailable)},
		{"bundle_id", mobileSource},
		{"cp_ct", strconv.Itoa(c.device.cpuCount)},
		{"cp_tp", c.device.cpuType},
		{"cp_us", c.device.cpuUsage},
		{"d_n", c.device.phoneModel},
		{"fr_mem", strconv.Itoa(c.device.freeMemoryMegabytes)},
		{"fr_st", strconv.Itoa(c.device.freeStorageMegabytes)},
		{"latitude", "0.0"},
		{"longitude", "0.0"},
		{"nt_st", bool01(c.device.notificationEnabled)},
		{"ph_br", c.device.phoneBrand},
		{"ph_md", c.device.phoneModel},
		{"ph_os", "Android " + c.device.androidRelease},
		{"pre_install", "InterfaceIsNull"},
		{"tt_mem", strconv.Itoa(c.device.totalMemoryMegabytes)},
		{"tt_st", strconv.Itoa(c.device.totalStorageMegabytes)},
		{"tz_of", strconv.FormatInt(c.device.timezoneOffsetSeconds, 10)},
		{"zx_expired", "0"},
	})
}

// formEncodePairs percent-encodes ordered form pairs the way Ktor's
// formUrlEncode does: insertion order, '+' for space, '%XX' (uppercase) for
// the rest, with the unreserved set '-_.~' left alone. This is byte-identical
// to the Android client's body and is what the HMAC is computed over.
func formEncodePairs(pairs [][2]string) string {
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(p[0]))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(p[1]))
	}
	return b.String()
}

func bool01(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func normalizePhoneNumber(phoneNumber string) (string, error) {
	compact := strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(phoneNumber))
	local := compact
	switch {
	case strings.HasPrefix(compact, "+86"):
		local = strings.TrimPrefix(compact, "+86")
	case strings.HasPrefix(compact, "86") && len(compact) == 13:
		local = strings.TrimPrefix(compact, "86")
	}
	if len(local) != 11 || !allDigits(local) || !strings.HasPrefix(local, "1") {
		return "", errors.New("请输入正确的中国大陆手机号")
	}
	return "+86" + local, nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func captchaInvalid(code int) bool {
	return code == 120001 || code == 120002
}

// extractZhihuErrorCode / extractZhihuErrorMessage read the account API error
// envelope ("error": {code, message}) with a fallback to the root object.
func extractZhihuErrorCode(body []byte) int {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return 0
	}
	if e, ok := m["error"]; ok {
		var em struct {
			Code int `json:"code"`
		}
		if json.Unmarshal(e, &em) == nil && em.Code != 0 {
			return em.Code
		}
	}
	var code int
	_ = json.Unmarshal(m["code"], &code)
	return code
}

func extractZhihuErrorMessage(body []byte) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return ""
	}
	if e, ok := m["error"]; ok {
		var em struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(e, &em) == nil && em.Message != "" {
			return em.Message
		}
	}
	var msg string
	_ = json.Unmarshal(m["message"], &msg)
	return msg
}

// ---- request plumbing ----

func (c *zhihuPhoneClient) newRequest(method, u string, body io.Reader) *http.Request {
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		panic(err.Error())
	}
	return req
}

func (c *zhihuPhoneClient) applyPhoneHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", zhihuAndroidPhoneUA)
	req.Header.Set("Authorization", c.authorization)
	if cookie := cookieString(c.cookies); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	if c.deviceID != "" {
		req.Header.Set("x-udid", c.deviceID)
	}
	req.Header.Set("x-api-version", "3.0.93")
	req.Header.Set("x-app-version", "11.4.0")
	req.Header.Set("x-app-build", "release")
	req.Header.Set("x-app-bundleid", mobileSource)
	req.Header.Set("x-app-flavor", "honor")
	req.Header.Set("x-app-za",
		"OS=Android&Release=12&Model=Android+SDK+built+for+arm64&VersionName=11.4.0&VersionCode=40408&"+
			"Product=com.zhihu.android&Width=1080&Height=2274&Installer=%E8%8D%A3%E8%80%80%E5%95%86%E5%BA%97&"+
			"DeviceType=AndroidPhone&Brand=Android")
	req.Header.Set("x-network-type", "3G")
	req.Header.Set("x-page-id", "44")
	req.Header.Set("x-zse-93", "101_1_1.0")
}

// encryptedPost sends one api.zhihu.com form body with the phone headers and
// the encrypted body, returning the raw bytes + status. For the device-guest
// init the caller adds the cloud-signature headers afterwards.
func (c *zhihuPhoneClient) encryptedPost(u, form string) ([]byte, int, error) {
	req := c.newRequest(http.MethodPost, u, bytes.NewReader([]byte(c.enc.encryptForm(form))))
	c.applyPhoneHeaders(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if strings.Contains(u, deviceGuestInitPath) {
		timestamp := strconv.FormatInt(c.nowEpoch(), 10)
		req.Header.Set("x-app-id", cloudAppID)
		req.Header.Set("x-sign-version", cloudSignVersion)
		req.Header.Set("x-req-ts", timestamp)
		req.Header.Set("x-req-signature",
			hmacSHA1Hex(cloudAppSecret, cloudAppID+cloudSignVersion+form+timestamp))
	}
	return c.do(req)
}

func (c *zhihuPhoneClient) do(req *http.Request) ([]byte, int, error) {
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	c.ingestServerCookies(resp.Header)
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}

// ingestServerCookies folds only the risk-captcha ticket back into the
// phone jar, mirroring the Ktor cookie engine the Android client relies on.
// Everything else is deliberately dropped: in particular the web d_c0 must
// not enter the jar before sign-in.
func (c *zhihuPhoneClient) ingestServerCookies(h http.Header) {
	for _, sc := range h.Values("Set-Cookie") {
		if kv, ok := cookiePair(sc); ok && kv[0] == "capsion_ticket" && kv[1] != "" {
			c.cookies["capsion_ticket"] = kv[1]
		}
	}
}

// cookiePair splits the first "name=value" token of a Set-Cookie header.
func cookiePair(sc string) (kv [2]string, ok bool) {
	sc = strings.TrimSpace(sc)
	if sc == "" {
		return kv, false
	}
	if i := strings.IndexByte(sc, ';'); i >= 0 {
		sc = sc[:i]
	}
	name, value, found := strings.Cut(sc, "=")
	if !found {
		return kv, false
	}
	return [2]string{strings.TrimSpace(name), strings.Trim(strings.TrimSpace(value), `"`)}, true
}

// ingestCaptchaTicketBody is the fallback when the ticket is not delivered as
// a Set-Cookie header: the GET /captcha response echoes it in its body
// "cookie" field. Callers merge it whenever the jar has no ticket yet.
func (c *zhihuPhoneClient) ingestCaptchaTicketBody(v string) {
	if c.cookies["capsion_ticket"] != "" || v == "" {
		return
	}
	if kv, ok := cookiePair(v); ok && kv[0] == "capsion_ticket" {
		c.cookies["capsion_ticket"] = kv[1]
	}
}

func cookieString(m map[string]string) string {
	var b strings.Builder
	for k, v := range m {
		if v == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(v)
	}
	return b.String()
}
