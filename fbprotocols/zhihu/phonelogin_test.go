package zhihu

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The following constants and scenarios mirror zhihu-plus-plus
// ZhihuPhoneLoginClientTest (AGPL-3.0) 1:1. They pin the device/time so the
// guest-init HMAC and the sign-in signature check out against the official
// known-answer strings, and they exercise the exact request sequence the
// Android client produces.

const phoneDeviceForm = "app_build=40408&app_install_time=1700000000000&app_ticket=interface+is+empty&" +
	"app_version=11.4.0&bt_ck=1&bundle_id=com.zhihu.android&cp_ct=4&cp_tp=aarch64&cp_us=0.0&" +
	"d_n=Android+SDK+built+for+arm64&fr_mem=128&fr_st=32768&latitude=0.0&longitude=0.0&nt_st=0&" +
	"ph_br=Android&ph_md=Android+SDK+built+for+arm64&ph_os=Android+12&pre_install=InterfaceIsNull&" +
	"tt_mem=256&tt_st=65536&tz_of=28800&zx_expired=0"

const phoneGuestInitSig = "32a3795d4beccbacb11cb806d67e155158e3566a"
const phoneSignInSig = "096465d8a44361e0393c87ab61b0d48a088b2cfb"
const phoneGuestInitResponse = `{"udid":"device-id","guest":{"access_token":"guest-access","token_type":"bearer","cookie":{"q_c0":"guest-cookie"}}}`
const phoneSimpleGuestResponse = `{"udid":"device-id","guest":{"access_token":"guest-access","token_type":"bearer"}}`

const phoneDigitsForm = "username=%2B8613800138000&client_id=8d5227e0aaaa4797a763ac64e0c3b8"
const phoneSignInForm = "client_id=8d5227e0aaaa4797a763ac64e0c3b8&digits=123456&grant_type=digits&" +
	"signature=096465d8a44361e0393c87ab61b0d48a088b2cfb&source=com.zhihu.android&" +
	"timestamp=1700000000&username=%2B8613800138000"
const phoneTokenResponse = `{"access_token":"account-access","refresh_token":"account-refresh",` +
	`"token_type":"bearer","expires_in":3600,"cookie":{"q_c0":"account-q-cookie","z_c0":"account-z-cookie"}}`

type phoneReply struct {
	body       string
	code       int
	setCookies []string
}

func prOK(body string) *phoneReply { return &phoneReply{body: body, code: http.StatusOK} }

// phoneTestServer script-swaps the package HTTP client and pins the phone
// clock/device/cookie overrides so the protocol runs against the corpus
// fixtures. The returned cleanup restores the real client.
func phoneTestServer(t *testing.T, cookies map[string]string, script func(i int, req *http.Request) *phoneReply) func() {
	t.Helper()
	dev := phoneDeviceInfoFromCorpus()
	clearPhoneOverrides()
	phoneLoginTestOverrides.mu.Lock()
	phoneLoginTestOverrides.cookies = cookies
	phoneLoginTestOverrides.device = &dev
	phoneLoginTestOverrides.nowFn = func() int64 { return 1700000000 }
	phoneLoginTestOverrides.mu.Unlock()

	oldHC := hc
	var i int
	hc = &http.Client{Transport: stubTransport(func(req *http.Request) (*http.Response, error) {
		i++
		var reply *phoneReply
		if script != nil {
			reply = script(i, req)
		}
		if reply == nil {
			reply = &phoneReply{body: "{}", code: http.StatusOK}
		}
		resp := &http.Response{
			StatusCode: reply.code,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(reply.body)),
			Request:    req,
		}
		for _, sc := range reply.setCookies {
			resp.Header.Add("Set-Cookie", sc)
		}
		return resp, nil
	})}
	return func() { hc = oldHC }
}

func clearPhoneOverrides() {
	phoneLoginTestOverrides.mu.Lock()
	phoneLoginTestOverrides.cookies = nil
	phoneLoginTestOverrides.device = nil
	phoneLoginTestOverrides.nowFn = nil
	phoneLoginTestOverrides.mu.Unlock()
}

func phoneDeviceInfoFromCorpus() phoneLoginDeviceInfo {
	return phoneLoginDeviceInfo{
		timezoneOffsetSeconds: 28800,
		appInstallTimeMillis:  1700000000000,
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

func reqBody(t *testing.T, req *http.Request) string {
	t.Helper()
	b, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	return string(b)
}

func assertHas(t *testing.T, s, want string) {
	t.Helper()
	if !strings.Contains(s, want) {
		t.Fatalf("expected %q to contain %q", s, want)
	}
}

func TestNormalizePhoneNumber(t *testing.T) {
	for in, want := range map[string]string{
		"13800138000":    "+8613800138000",
		"138 0013 8000":  "+8613800138000",
		"138-0013-8000":  "+8613800138000",
		"+8613800138000": "+8613800138000",
		"8613800138000":  "+8613800138000",
		" 13800138000 ":  "+8613800138000",
	} {
		got, err := normalizePhoneNumber(in)
		if err != nil {
			t.Fatalf("normalize(%q) unexpected err: %v", in, err)
		}
		if got != want {
			t.Fatalf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"1380013800", "138001380001", "23800138000", "", "abc", "+861 380013800"} {
		if _, err := normalizePhoneNumber(bad); err == nil {
			t.Fatalf("normalize(%q) should fail", bad)
		}
	}
}

func TestFormEncodeMatchesCorpus(t *testing.T) {
	// The device fields (pinned to the corpus device) must encode to the exact
	// string the Android client submits.
	dev := phoneDeviceInfoFromCorpus()
	c := &zhihuPhoneClient{device: dev}
	if got := c.deviceForm(); got != phoneDeviceForm {
		t.Fatalf("deviceForm = %s", got)
	}
	// The digits and sign-in forms must match the corpus strings byte-for-byte.
	if got := formEncodePairs([][2]string{{"username", "+8613800138000"}, {"client_id", mobileClientID}}); got != phoneDigitsForm {
		t.Fatalf("digits form = %s", got)
	}
	sig := hmacSHA1Hex(mobileClientSecret, digitsGrantType+mobileClientID+mobileSource+"1700000000")
	if got := formEncodePairs([][2]string{
		{"client_id", mobileClientID},
		{"digits", "123456"},
		{"grant_type", digitsGrantType},
		{"signature", sig},
		{"source", mobileSource},
		{"timestamp", "1700000000"},
		{"username", "+8613800138000"},
	}); got != phoneSignInForm {
		t.Fatalf("sign-in form = %s", got)
	}
}

func TestHmacSignaturesMatchOfficialVectors(t *testing.T) {
	// guest-init: HMAC(cloud-secret, "1355"+"2"+deviceForm+"1700000000")
	if got := hmacSHA1Hex(cloudAppSecret, cloudAppID+cloudSignVersion+phoneDeviceForm+"1700000000"); got != phoneGuestInitSig {
		t.Fatalf("guest-init signature = %s, want %s", got, phoneGuestInitSig)
	}
	// sign-in: HMAC(mobile-secret, "digits"+clientId+source+"1700000000")
	if got := hmacSHA1Hex(mobileClientSecret, digitsGrantType+mobileClientID+mobileSource+"1700000000"); got != phoneSignInSig {
		t.Fatalf("sign-in signature = %s, want %s", got, phoneSignInSig)
	}
}

func TestEncryptorProtoDataLayout(t *testing.T) {
	enc := newBodyEncryptor()
	if len(enc.roundKeys) != 176 || len(enc.x0) != 256 || len(enc.xr) != 256 || len(enc.xf) != 256 || len(enc.sbox) != 256 {
		t.Fatalf("bad proto table sizes")
	}
	for i, tb := range enc.tables {
		if len(tb) != 1024 {
			t.Fatalf("table t%d len = %d, want 1024", i, len(tb))
		}
	}
	// Encryption is deterministic for the same form.
	a := enc.encryptForm(phoneDeviceForm)
	b := enc.encryptForm(phoneDeviceForm)
	if a != b || a == "" {
		t.Fatalf("encryptForm not deterministic")
	}
}

func TestCaptchaSessionV2CapturedAndSent(t *testing.T) {
	resetSessionCreds()
	defer resetSessionCreds()
	hdr := http.Header{}
	hdr.Set("Set-Cookie", "captcha_session_v2=abc123xyz; Path=/; Domain=zhihu.com")
	sess.captureCookies(hdr)
	sess.mu.RLock()
	got := sess.capSession
	sess.mu.RUnlock()
	if got != "abc123xyz" {
		t.Fatalf("capSession = %q, want abc123xyz", got)
	}
	cookie := sess.cookieHeader()
	assertHas(t, cookie, "captcha_session_v2=abc123xyz")
}

// ---- protocol scenarios (mirror of ZhihuPhoneLoginClientTest) ----

func TestPhoneGuestFlowUnionsDigitsAndCookies(t *testing.T) {
	resetSessionCreds()
	resetLoginGateway()
	resetRateGate()
	defer resetSessionCreds()
	defer clearPhoneOverrides()
	defer resetPhoneClient()

	cookies := map[string]string{}
	enc := newBodyEncryptor()
	var reqs []*http.Request

	tc := phoneTestServer(t, cookies, func(i int, req *http.Request) *phoneReply {
		reqs = append(reqs, req)
		switch i {
		case 1:
			return &phoneReply{body: "web-device-id", code: http.StatusOK,
				setCookies: []string{"d_c0=web-device-cookie; Path=/; Domain=zhihu.com"}}
		case 2:
			if req.URL.Host != "api.zhihu.com" || req.URL.Path != deviceGuestInitPath {
				t.Fatalf("req2 url = %s", req.URL)
			}
			if got := req.Header.Get("Authorization"); got != "oauth "+mobileClientID {
				t.Fatalf("req2 Authorization = %q", got)
			}
			if got := req.Header.Get("x-app-id"); got != cloudAppID {
				t.Fatalf("req2 x-app-id = %q", got)
			}
			if got := req.Header.Get("x-sign-version"); got != cloudSignVersion {
				t.Fatalf("req2 x-sign-version = %q", got)
			}
			if got := req.Header.Get("x-req-ts"); got != "1700000000" {
				t.Fatalf("req2 x-req-ts = %q", got)
			}
			if got := req.Header.Get("x-req-signature"); got != phoneGuestInitSig {
				t.Fatalf("req2 x-req-signature = %q, want %s (form/hmac mismatch)", got, phoneGuestInitSig)
			}
			if cc := req.Header.Get("Cookie"); strings.Contains(cc, "d_c0=") {
				t.Fatalf("req2 cookie leaked d_c0: %q", cc)
			}
			assertHas(t, req.Header.Get("User-Agent"), "Android SDK built for arm64")
			assertHas(t, req.Header.Get("x-app-za"), "Width=1080&Height=2274")
			if got := enc.encryptForm(phoneDeviceForm); got != reqBody(t, req) {
				t.Fatalf("req2 guest-init body = %s, want encrypt(deviceForm)", got)
			}
			return prOK(phoneGuestInitResponse)
		case 3:
			if req.Method != http.MethodGet || req.URL.Path != captchaPath {
				t.Fatalf("req3 = %s %s", req.Method, req.URL.Path)
			}
			return prOK(`{"show_captcha":false}`)
		case 4:
			if got := req.Header.Get("Authorization"); got != "bearer guest-access" {
				t.Fatalf("req4 Authorization = %q", got)
			}
			assertHas(t, req.Header.Get("Cookie"), "q_c0=guest-cookie")
			if got := enc.encryptForm(phoneDigitsForm); got != reqBody(t, req) {
				t.Fatalf("req4 digits body = %s, want encrypt(digitsForm)", got)
			}
			return prOK(`{"success":true}`)
		default:
			t.Fatalf("unexpected request #%d: %s %s", i+1, req.Method, req.URL)
		}
		return &phoneReply{body: "", code: http.StatusOK}
	})
	defer tc()

	outcome, err := zhihuPhoneSendDigits("138 0013 8000")
	if err != nil {
		t.Fatalf("requestDigits err = %v", err)
	}
	if !outcome.Sent {
		_ = outcome
		t.Fatalf("expected Sent, got captcha")
	}
	if len(reqs) != 4 {
		t.Fatalf("request count = %d, want 4", len(reqs))
	}
	if _, ok := cookies["d_c0"]; ok {
		t.Fatalf("d_c0 leaked into phone jar")
	}
	if cookies["q_c0"] != "guest-cookie" {
		t.Fatalf("q_c0 = %q, want guest-cookie", cookies["q_c0"])
	}
}
func TestPhoneCaptchaBranchShowsImageAndVerifiesThenSends(t *testing.T) {
	resetSessionCreds()
	resetLoginGateway()
	resetRateGate()
	defer resetSessionCreds()
	defer clearPhoneOverrides()
	defer resetPhoneClient()

	cookies := map[string]string{"d_c0": "device-cookie"}
	enc := newBodyEncryptor()
	var reqs []*http.Request

	tc := phoneTestServer(t, cookies, func(i int, req *http.Request) *phoneReply {
		reqs = append(reqs, req)
		switch i {
		case 1:
			return prOK(phoneSimpleGuestResponse)
		case 2:
			if req.Method != http.MethodGet || req.URL.Path != captchaPath {
				t.Fatalf("req2 = %s %s", req.Method, req.URL.Path)
			}
			return prOK(`{"show_captcha":true}`)
		case 3:
			if req.Method != http.MethodPut || req.URL.Path != captchaPath {
				t.Fatalf("req3 = %s %s", req.Method, req.URL.Path)
			}
			return prOK(`{"show_captcha":true,"img_base64":"image-data"}`)
		case 4:
			if req.Method != http.MethodPost || req.URL.Path != captchaPath {
				t.Fatalf("req4 = %s %s", req.Method, req.URL.Path)
			}
			want := enc.encryptForm(formEncodePairs([][2]string{{"input_text", "a7Bc"}}))
			if got := reqBody(t, req); got != want {
				t.Fatalf("req4 captcha body = %s, want %s", got, want)
			}
			return prOK(`{"success":true}`)
		case 5:
			if req.URL.Path != captchaPath {
				t.Fatalf("req5 = %s %s", req.Method, req.URL.Path)
			}
			return prOK(`{"show_captcha":false}`)
		case 6:
			if req.URL.Path != authDigitsPath {
				t.Fatalf("req6 = %s %s", req.Method, req.URL.Path)
			}
			if got := enc.encryptForm(phoneDigitsForm); got != reqBody(t, req) {
				t.Fatalf("req6 digits body mismatch")
			}
			return prOK(`{"success":true}`)
		default:
			t.Fatalf("unexpected request #%d: %s %s", i+1, req.Method, req.URL)
		}
		return &phoneReply{body: "", code: http.StatusOK}
	})
	defer tc()

	outcome, err := zhihuPhoneSendDigits("13800138000")
	if err != nil {
		t.Fatalf("requestDigits err = %v", err)
	}
	if !outcome.Captcha {
		t.Fatalf("expected CaptchaRequired")
	}
	if outcome.ImageB64 != "data:image/png;base64,image-data" {
		t.Fatalf("image = %q", outcome.ImageB64)
	}
	out, err := zhihuPhoneVerifyCaptcha("13800138000", " a7Bc ")
	if err != nil {
		t.Fatalf("verifyCaptcha err = %v", err)
	}
	if !out.Sent {
		t.Fatalf("expected Sent after verify")
	}
	if len(reqs) != 6 {
		t.Fatalf("request count = %d, want 6", len(reqs))
	}
}

func TestPhoneCompleteLoginKeepsWebDeviceCookie(t *testing.T) {
	resetSessionCreds()
	resetLoginGateway()
	resetRateGate()
	defer resetSessionCreds()
	defer clearPhoneOverrides()
	defer resetPhoneClient()

	cookies := map[string]string{}
	enc := newBodyEncryptor()
	var reqs []*http.Request

	tc := phoneTestServer(t, cookies, func(i int, req *http.Request) *phoneReply {
		reqs = append(reqs, req)
		switch i {
		case 1:
			return &phoneReply{body: "web-device-id", code: http.StatusOK,
				setCookies: []string{"d_c0=device-cookie; Path=/; Domain=zhihu.com"}}
		case 2:
			return prOK(phoneGuestInitResponse)
		case 3:
			return prOK(`{"show_captcha":false}`)
		case 4:
			return prOK(`{"success":true}`)
		case 5:
			if req.URL.Host != "api.zhihu.com" || req.URL.Path != signInPath {
				t.Fatalf("req5 url = %s", req.URL)
			}
			if got := req.Header.Get("Authorization"); got != "bearer guest-access" {
				t.Fatalf("req5 Authorization = %q", got)
			}
			assertHas(t, req.Header.Get("Cookie"), "q_c0=guest-cookie")
			if cc := req.Header.Get("Cookie"); strings.Contains(cc, "d_c0=") {
				t.Fatalf("req5 cookie leaked d_c0: %q", cc)
			}
			if got := enc.encryptForm(phoneSignInForm); got != reqBody(t, req) {
				t.Fatalf("req5 sign-in body mismatch (want encrypt(signInForm with official sig))")
			}
			return prOK(phoneTokenResponse)
		default:
			t.Fatalf("unexpected request #%d: %s %s", i+1, req.Method, req.URL)
		}
		return &phoneReply{body: "", code: http.StatusOK}
	})
	defer tc()

	outcome, err := zhihuPhoneSendDigits("13800138000")
	if err != nil {
		t.Fatalf("requestDigits err = %v", err)
	}
	if !outcome.Sent {
		t.Fatalf("expected Sent")
	}
	token, err := zhihuPhoneSignIn("+8613800138000", "123456")
	if err != nil {
		t.Fatalf("signIn err = %v", err)
	}
	if token.AccessToken != "account-access" {
		t.Fatalf("accessToken = %q", token.AccessToken)
	}
	if token.ExpiresAt != 1700003600 {
		t.Fatalf("expiresAt = %d", token.ExpiresAt)
	}
	if token.Cookies["d_c0"] != "device-cookie" || token.Cookies["q_c0"] != "account-q-cookie" || token.Cookies["z_c0"] != "account-z-cookie" {
		t.Fatalf("token cookies = %v", token.Cookies)
	}
	if len(reqs) != 5 {
		t.Fatalf("request count = %d, want 5", len(reqs))
	}
}

func TestPhoneUdidWithoutDeviceCookieStops(t *testing.T) {
	resetSessionCreds()
	resetLoginGateway()
	resetRateGate()
	defer resetSessionCreds()
	defer clearPhoneOverrides()
	defer resetPhoneClient()

	cookies := map[string]string{}
	var reqs []*http.Request
	tc := phoneTestServer(t, cookies, func(i int, req *http.Request) *phoneReply {
		reqs = append(reqs, req)
		return &phoneReply{body: "web-device-id", code: http.StatusOK}
	})
	defer tc()

	_, err := zhihuPhoneSendDigits("13800138000")
	if err == nil {
		t.Fatal("expected error when /udid sets no d_c0")
	}
	if !strings.Contains(err.Error(), "服务器未返回必要的 Cookie d_c0") {
		t.Fatalf("err = %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("request count = %d, want 1", len(reqs))
	}
}

func TestPhoneDigitsSurfacesServerMessageForBadPhone(t *testing.T) {
	resetSessionCreds()
	resetLoginGateway()
	resetRateGate()
	defer resetSessionCreds()
	defer clearPhoneOverrides()
	defer resetPhoneClient()

	cookies := map[string]string{"d_c0": "device-cookie"}
	tc := phoneTestServer(t, cookies, func(i int, req *http.Request) *phoneReply {
		switch i {
		case 1:
			return prOK(phoneSimpleGuestResponse)
		case 2:
			return prOK(`{"show_captcha":false}`)
		case 3:
			return &phoneReply{body: `{"error":{"code":100030,"name":"ERR_BAD_PHONE_NO_FORMAT","message":"手机号格式错误"}}`, code: http.StatusBadRequest}
		default:
			t.Fatalf("unexpected request #%d", i+1)
		}
		return &phoneReply{body: "", code: http.StatusOK}
	})
	defer tc()

	_, err := zhihuPhoneSendDigits("13800138000")
	if err == nil {
		t.Fatal("expected error for bad phone")
	}
	if !strings.Contains(err.Error(), "手机号格式错误") {
		t.Fatalf("err = %v", err)
	}
}

func TestPhoneDigits120005RetriesOnce(t *testing.T) {
	resetSessionCreds()
	resetLoginGateway()
	resetRateGate()
	defer resetSessionCreds()
	defer clearPhoneOverrides()
	defer resetPhoneClient()

	cookies := map[string]string{"d_c0": "device-cookie"}
	var reqs []*http.Request
	tc := phoneTestServer(t, cookies, func(i int, req *http.Request) *phoneReply {
		reqs = append(reqs, req)
		switch i {
		case 1:
			return prOK(phoneSimpleGuestResponse)
		case 2:
			return prOK(`{"show_captcha":false}`)
		case 3:
			return &phoneReply{body: `{"error":{"code":120005,"message":"需要重新检查验证码"}}`, code: http.StatusBadRequest}
		case 4:
			if req.URL.Path != captchaPath {
				t.Fatalf("req4 = %s %s", req.Method, req.URL.Path)
			}
			return prOK(`{"show_captcha":false}`)
		case 5:
			if req.URL.Path != authDigitsPath {
				t.Fatalf("req5 = %s %s", req.Method, req.URL.Path)
			}
			return prOK(`{"success":true}`)
		default:
			t.Fatalf("unexpected request #%d", i+1)
		}
		return &phoneReply{body: "", code: http.StatusOK}
	})
	defer tc()

	outcome, err := zhihuPhoneSendDigits("13800138000")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !outcome.Sent {
		t.Fatalf("expected Sent after 120005 retry")
	}
	if len(reqs) != 5 {
		t.Fatalf("request count = %d, want 5", len(reqs))
	}
}
