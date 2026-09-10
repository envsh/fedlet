package zhihu

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	zhihuWebUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"
	apiReferer = "https://www.zhihu.com/hot"

	// signinReferer/zhihuSigninURL mirror the zhihu-plus-plus web login
	// ceremony: non-polling login calls refer to /signin, polling uses the
	// "?next=%2F" variant, and GET /signin primes the real _xsrf cookie.
	signinReferer  = "https://www.zhihu.com/signin"
	zhihuSigninURL = "https://www.zhihu.com/signin?next=%2F"

	secChUa         = `"Not:A-Brand";v="99", "Google Chrome";v="145", "Chromium";v="145"`
	secChUaMobile   = "?0"
	secChUaPlatform = `"Windows"`

	// captchaV2URL is the 2026 login captcha bootstrap (sign-in type); it
	// issues the captcha_session_v2 cookie used to ticket the QR/phone flows.
	captchaV2URL = "https://www.zhihu.com/api/v3/oauth/captcha/v2?type=captcha_sign_in"
)

// ErrNotLoggedIn is returned when an authenticated endpoint rejects the stored
// session (HTTP 401 / "未登录"). It triggers the expired-session handler but
// is not a network or rate-limit failure.
var ErrNotLoggedIn = errors.New("zhihu: not logged in or session expired")

// errUnverifiable marks a 200 response carrying an HTML page (verification /
// risk-control / logout) instead of the expected JSON. It is classified by
// errors.Is at the session probe and the feed error routing.
var errUnverifiable = errors.New("zhihu: unverifiable html response")

// looksHTML reports whether a response body starts like an HTML document.
func looksHTML(b []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(b), []byte{'<'})
}

// hc is the package-wide HTTP client. It keeps no cookie jar: cookies are
// injected explicitly per request from webSession so the d_c0 used in the
// signature always matches the one sent to the server.
var hc = func() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			prev := req.Response
			if prev != nil {
				log.Printf("zhihu: redirect %s %s -> %d Location=%s Set-Cookie=[%s]",
					prev.Request.Method, prev.Request.URL.String(), prev.StatusCode,
					prev.Header.Get("Location"), setCookieNames(prev.Header))
				sess.captureCookies(prev.Header)
				// The anti-bot hop: /signin 3xx → /account/unhuman (human
				// verification). It finally lands on a 200 page, so the final
				// status alone cannot gate it — record the first hit instead.
				if loc := prev.Header.Get("Location"); strings.Contains(loc, "account/unhuman") {
					sess.mu.Lock()
					if sess.qrGate == 0 {
						sess.qrGate = prev.StatusCode
						sess.qrRiskURL = loc
					}
					sess.mu.Unlock()
				}
			}
			if len(via) >= 10 {
				return errors.New("zhihu: too many redirects")
			}
			return nil
		},
		Transport: &http.Transport{
			ForceAttemptHTTP2:   true,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}
}()

func setCookieNames(h http.Header) string {
	var names []string
	for _, c := range h.Values("Set-Cookie") {
		name, _, ok := strings.Cut(c, "=")
		if !ok {
			continue
		}
		names = append(names, strings.TrimSpace(name))
	}
	return strings.Join(names, " ")
}

const dc0Alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// webSession holds the cookies shared by all web requests: d_c0 (visitor
// token, present anonymous and feeding the signature) plus the login session
// z_c0 / _xsrf when authenticated.
type webSession struct {
	mu         sync.RWMutex
	dC0        string
	zC0        string
	xsrf       string
	zap        string
	qC1        string
	capsion    string
	capSession string // captcha_session_v2: the 2026 login ticket from captcha/v2
	bec        string // BEC: AB-test cookie set on every page; zhihu++ jar forwards it
	secTok     string // sec_token: anti-bot 302 beacon — presence ⇒ env verification required
	qC1Real    bool   // q_c1 came from the server (/udid) rather than the newQC1() fallback
	dC0Real    bool   // d_c0 was issued by zhihu (Set-Cookie) rather than the newDC0() bootstrap
	qrGate     int    // 3xx recorded at an /account/unhuman redirect hop ⇒ QR begin skipped
	qrRiskURL  string // the /account/unhuman verification URL the user must open
	user       string
	status     string
}

var sess = &webSession{dC0: newDC0(), status: AuthStatusEmpty}

func newDC0() string {
	var b strings.Builder
	b.WriteByte('A')
	for i := 0; i < 26; i++ {
		b.WriteByte(dc0Alpha[rand.Intn(len(dc0Alpha))])
	}
	b.WriteByte('|')
	b.WriteString(fmt.Sprintf("%d|%s", time.Now().Unix(), randHex(6)))
	return b.String()
}

func randHex(n int) string {
	const hx = "0123456789abcdef"
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(hx[rand.Intn(len(hx))])
	}
	return b.String()
}

// newQC1 fabricates a visitor-side q_c1 cookie (a random base64-ish token the
// web front-end keeps; server treats it as opaque but requires it present with
// a matching x-zse-96 signature).
func newQC1() string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/="
	var b strings.Builder
	b.WriteByte('Q')
	for i := 0; i < 32; i++ {
		b.WriteByte(alphabet[rand.Intn(len(alphabet))])
	}
	return b.String()
}

// loginCookieHeader is the same jar minus the fabricated q_c1: the login
// ceremony (/signin, /udid, captcha/v2, /qrcode, scan_info) strictly validates
// q_c1, so a server-minted one is forwarded but the newQC1() fallback is not
// (it answers HTTP 400 code 1000). The data APIs keep cookieHeader so their
// x-zse-96 signature stays consistent with the sent cookie.
func (s *webSession) loginCookieHeader() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var b strings.Builder
	// Only server-issued identity is forwarded on the login ceremony: a
	// fabricated d_c0 / q_c1 (or a stale sec_token from an earlier risk round)
	// makes zhihu 302 the whole session to /account/unhuman and we never get
	// real tickets — a fresh request with no identity rides the clean 200.
	if s.dC0Real {
		b.WriteString("d_c0=" + s.dC0)
	}
	if s.zap != "" {
		b.WriteString("; _zap=" + s.zap)
	}
	if s.qC1 != "" && s.qC1Real {
		b.WriteString("; q_c1=" + s.qC1)
	}
	if s.zC0 != "" {
		b.WriteString("; z_c0=" + s.zC0)
	}
	if s.xsrf != "" {
		b.WriteString("; _xsrf=" + s.xsrf)
	}
	if s.capsion != "" {
		b.WriteString("; capsion_ticket=" + s.capsion)
	}
	if s.capSession != "" {
		b.WriteString("; captcha_session_v2=" + s.capSession)
	}
	if s.bec != "" {
		b.WriteString("; BEC=" + s.bec)
	}
	return b.String()
}

func (s *webSession) cookieHeader() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var b strings.Builder
	b.WriteString("d_c0=" + s.dC0)
	if s.zap != "" {
		b.WriteString("; _zap=" + s.zap)
	}
	if s.qC1 != "" {
		b.WriteString("; q_c1=" + s.qC1)
	}
	if s.zC0 != "" {
		b.WriteString("; z_c0=" + s.zC0)
	}
	if s.xsrf != "" {
		b.WriteString("; _xsrf=" + s.xsrf)
	}
	if s.capsion != "" {
		b.WriteString("; capsion_ticket=" + s.capsion)
	}
	if s.capSession != "" {
		b.WriteString("; captcha_session_v2=" + s.capSession)
	}
	if s.bec != "" {
		b.WriteString("; BEC=" + s.bec)
	}
	if s.secTok != "" {
		b.WriteString("; sec_token=" + s.secTok)
	}
	return b.String()
}

func (s *webSession) signDC0() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dC0
}

// cookieState reports which shared cookies are present, for diagnosing the QR
// login prerequisites (missing q_c1 / captcha_session_v2 → 400 code 1000).
func (s *webSession) cookieState() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	dc0 := "absent"
	switch {
	case s.dC0 == "":
		dc0 = "absent"
	case s.dC0Real:
		dc0 = "real"
	default:
		dc0 = "fake"
	}
	qc1 := "absent"
	switch {
	case s.qC1 == "":
		qc1 = "absent"
	case s.qC1Real:
		qc1 = "real"
	default:
		qc1 = "fake"
	}
	return fmt.Sprintf("d_c0=%s q_c1=%s _xsrf=%t captcha_session_v2=%t zap=%t z_c0=%t bec=%t sec_token=%t qr_gate=%d",
		dc0, qc1, s.xsrf != "", s.capSession != "", s.zap != "", s.zC0 != "",
		s.bec != "", s.secTok != "", s.qrGate)
}

func (s *webSession) hasZ() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.zC0 != ""
}

func (s *webSession) getXsrf() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.xsrf
}

func (s *webSession) qrGateStatus() (int, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.qrGate, s.qrRiskURL
}

func (s *webSession) getQrRiskURL() (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.qrRiskURL, s.qrRiskURL != ""
}

// setZC0 stores a z_c0 session cookie parsed out of a scan_info body.
func (s *webSession) setZC0(v string) {
	if v == "" {
		return
	}
	s.mu.Lock()
	s.zC0 = v
	s.mu.Unlock()
}

// captureCookies stores z_c0 / _xsrf / d_c0 found in a Set-Cookie batch.
func (s *webSession) captureCookies(h http.Header) {
	for _, c := range h.Values("Set-Cookie") {
		name, value, _ := strings.Cut(c, "=")
		value, _, _ = strings.Cut(value, ";")
		switch name {
		case "z_c0":
			s.mu.Lock()
			if value != "" {
				s.zC0 = value
			}
			s.mu.Unlock()
		case "_xsrf":
			s.mu.Lock()
			if value != "" {
				s.xsrf = value
			}
			s.mu.Unlock()
		case "d_c0":
			s.mu.Lock()
			if value != "" {
				s.dC0 = value
				s.dC0Real = true
			}
			s.mu.Unlock()
		case "_zap":
			s.mu.Lock()
			if value != "" {
				s.zap = value
			}
			s.mu.Unlock()
		case "q_c1":
			s.mu.Lock()
			if value != "" {
				s.qC1 = value
				s.qC1Real = true
			}
			s.mu.Unlock()
		case "capsion_ticket":
			s.mu.Lock()
			if value != "" {
				s.capsion = value
			}
			s.mu.Unlock()
		case "captcha_session_v2":
			s.mu.Lock()
			if value != "" {
				s.capSession = value
			}
			s.mu.Unlock()
		case "BEC":
			s.mu.Lock()
			if value != "" {
				s.bec = value
			}
			s.mu.Unlock()
		case "sec_token":
			s.mu.Lock()
			if value != "" {
				s.secTok = value
			}
			s.mu.Unlock()
		}
	}
}

// getRaw performs an API request on www.zhihu.com: waits on the rate gate,
// injects the browser headers, the shared cookie and the x-zse-93/96 signature,
// classifies 403 (risk control -> backoff) and 401 (session expiry). It returns
// the raw body and HTTP status.
func getRaw(method, u string, body []byte, needAuth bool) ([]byte, int, error) {
	waitRateGate()

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, u, rdr)
	if err != nil {
		return nil, 0, err
	}
	if needAuth && !sess.hasZ() {
		return nil, http.StatusUnauthorized, ErrNotLoggedIn
	}
	// Warm up only for requests that actually reach the network (the anonymous
	// gate above returns first when unauthenticated).
	warmupOnce.Do(warmup)

	req.Header.Set("User-Agent", zhihuWebUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", apiReferer)
	req.Header.Set("Cookie", sess.cookieHeader())
	if xsrf := sess.getXsrf(); xsrf != "" {
		req.Header.Set("x-xsrftoken", xsrf)
	}
	if apiVersion != "" {
		req.Header.Set("x-api-version", apiVersion)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range signZhihuRequest(u, sess.signDC0(), string(body)) {
		req.Header.Set(k, v)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	sess.captureCookies(resp.Header)
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}

	switch resp.StatusCode {
	case http.StatusOK:
		if looksHTML(respBody) {
			noteRateLimit()
			return respBody, resp.StatusCode,
				fmt.Errorf("%w http 200: %s", errUnverifiable, truncate(string(respBody), 160))
		}
		clearRateLimit()
	case http.StatusUnauthorized:
		if !needAuth {
			return respBody, resp.StatusCode,
				fmt.Errorf("zhihu: http 401 %s", truncate(string(respBody), 160))
		}
		markSessionInvalid(errors.New("zhihu: session rejected (401)"))
		return respBody, resp.StatusCode, ErrNotLoggedIn
	case http.StatusForbidden:
		if isRiskControl(respBody) {
			noteRateLimit()
		} else {
			return respBody, resp.StatusCode, fmt.Errorf("zhihu: http %d %s", resp.StatusCode, truncate(string(respBody), 200))
		}
	}
	return respBody, resp.StatusCode, nil
}

// isRiskControl heuristically detects the Zhihu anti-crawler 403 page/фlags.
func isRiskControl(body []byte) bool {
	s := string(body)
	return strings.Contains(s, "10003") || strings.Contains(s, "security") ||
		strings.Contains(s, "验证") || len(body) < 200
}

// getJSON is a small helper: GET a signed endpoint, require 200 and unmarshal.
func getJSON(data any, u string, needAuth bool) error {
	body, status, err := getRaw(http.MethodGet, u, nil, needAuth)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("zhihu: http %d %s", status, truncate(string(body), 200))
	}
	if err := json.Unmarshal(body, data); err != nil {
		return fmt.Errorf("zhihu: parse %s: %w", u, err)
	}
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// ---- anonymous session warmup ----

var (
	apiVersion string
	warmupOnce sync.Once
)

var apiVersionRE = regexp.MustCompile(`__API_VERSION__\s*=\s*file\("([^"]+)"\)|__API_VERSION__\s*=\s*"([^"]+)"|x-api-version["']?\s*[:=]\s*["']([^"']+)["']`)

// warmup visits the zhihu.com login prerequisites once before any signed API
// call so the visitor cookies match the browser flow: GET /signin primes the
// real _xsrf/_zap, POST /udid the real q_c1/d_c0, and captcha/v2 the session
// ticket (QR/scan login answers 400 code 1000 without a real _xsrf). Any step
// may fail gracefully — the fallback generated q_c1/d_c0 still satisfy most
// endpoints. Note that a successful warmup is NOT an auth signal: it only
// establishes the anonymous visitor session. qrBegin additionally re-runs the
// same bootstrap per attempt via refreshLoginContext, because the one-time
// warmup values are typically stale by the time the user opens the login UI.
func warmup() {
	ok, status := primeLoginContext()
	log.Printf("zhihu: warmup ok=%v http=%d (anonymous visitor only, not auth) d_c0=%s", ok, status, shortDC0())
}

// refreshLoginContext mirrors zhihu-plus-plus prefetchQrLoginContext: it
// re-primes _xsrf/d_c0/captcha_session_v2 right before a QR begin so the POST
// /qrcode carries fresh tokens instead of the process-once warmup values
// (stale ones are answered HTTP 400 code 1000 "请求错误"). A non-200 /signin
// (anti-bot 302 + sec_token) returns false so the caller can stop before
// minting a poisoned session instead of POSTing /qrcode into a guaranteed 400.
func refreshLoginContext() bool {
	ok, status := primeLoginContext()
	log.Printf("zhihu: refresh login context ok=%v http=%d d_c0=%s", ok, status, shortDC0())
	return ok
}

// primeLoginContext runs the three-step visitor bootstrap shared by warmup and
// refreshLoginContext: GET /signin primes the real _xsrf/_zap, POST /udid the
// real q_c1/d_c0, and captcha/v2 the captcha_session_v2 ticket. When /signin
// returns anything other than 200 the anti-bot gate is up: the partially
// poisoned cookies are kept (for diagnosis) but udid/captcha are skipped and
// the caller should not attempt a login request this round.
func primeLoginContext() (bool, int) {
	// A fresh identity per attempt: a leftover gate, stale risk URL or an old
	// sec_token from a previous flagged round must never leak into this one.
	sess.mu.Lock()
	sess.qrGate = 0
	sess.qrRiskURL = ""
	sess.secTok = ""
	sess.mu.Unlock()
	req, err := http.NewRequest(http.MethodGet, zhihuSigninURL, nil)
	if err != nil {
		log.Printf("zhihu: warmup build request: %v", err)
		return false, 0
	}
	req.Header.Set("User-Agent", zhihuWebUA)
	req.Header.Set("sec-ch-ua", secChUa)
	req.Header.Set("sec-ch-ua-mobile", secChUaMobile)
	req.Header.Set("sec-ch-ua-platform", secChUaPlatform)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", "https://www.zhihu.com/") // zhihu++ createDesktopHeaders(ZHIHU_HOME_URL)
	req.Header.Set("Cookie", sess.loginCookieHeader())

	resp, err := hc.Do(req)
	if err != nil {
		log.Printf("zhihu: warmup get signin page: %v", err)
		return false, 0
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	sess.captureCookies(resp.Header)
	// The anti-bot gate: a redirect hop to /account/unhuman (recorded by
	// CheckRedirect) or a non-200 /signin means the visitor session is under
	// human verification — skip udid/captcha and let the caller stop instead
	// of minting a poisoned session and POSTing /qrcode into a guaranteed 400.
	gate, _ := sess.qrGateStatus()
	if resp.StatusCode != http.StatusOK || gate != 0 {
		if gate == 0 {
			sess.mu.Lock()
			sess.qrGate = resp.StatusCode
			sess.mu.Unlock()
		}
		log.Printf("zhihu: signin bootstrap: http %d qr_gate=%d Set-Cookie=[%s] — skip udid/captcha this round",
			resp.StatusCode, gate, setCookieNames(resp.Header))
		return false, gate
	}
	sess.mu.Lock()
	sess.qrGate = 0
	sess.qrRiskURL = ""
	sess.mu.Unlock()
	if v := resp.Header.Get("x-api-version"); v != "" {
		apiVersion = v
	}
	if m := apiVersionRE.FindSubmatch(body); m != nil {
		for _, g := range m[1:] {
			if len(g) > 0 {
				apiVersion = string(g)
				break
			}
		}
	}

	// POST /udid is how the web login mints real q_c1/d_c0 cookies.
	warmupPost("https://www.zhihu.com/udid")
	// captcha/v2 (sign-in type) issues captcha_session_v2, the 2026 ticket for
	// the QR/phone login flows (harmless to have for plain polling).
	warmupGet(captchaV2URL)

	sess.mu.Lock()
	if sess.qC1 == "" {
		sess.qC1 = newQC1()
	}
	sess.mu.Unlock()
	log.Printf("zhihu: login context cookies: %s", sess.cookieState())
	return true, http.StatusOK
}

func shortDC0() string {
	dc0 := sess.signDC0()
	if len(dc0) > 30 {
		dc0 = dc0[:30] + "..."
	}
	return dc0
}

// warmupGet/warmupPost run non-signed login-bootstrap requests with the full
// fetch-style headers (they must not go through getRaw, which re-enters warmup
// via sync.Once). Missing sec-ch-ua/Origin/x-requested-with made zhihu treat
// the /udid and /captcha/v2 steps as non-browser and withhold the q_c1 /
// captcha_session_v2 tickets, so POST /qrcode was answered HTTP 400 code 1000.
func warmupGet(u string) {
	if !warmupRequest(http.MethodGet, u, nil, "") {
		log.Printf("zhihu: warmup GET %s unavailable", u)
	}
}

func warmupPost(u string) {
	if !warmupRequest(http.MethodPost, u, strings.NewReader("{}"), "application/json") {
		log.Printf("zhihu: warmup POST %s unavailable", u)
	}
}

func warmupRequest(method, u string, body io.Reader, ctype string) bool {
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return false
	}
	for k, v := range loginFlowHeaders("https://www.zhihu.com/signin", false) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Cookie", sess.loginCookieHeader())
	resp, err := hc.Do(req)
	if err != nil {
		log.Printf("zhihu: warmup %s %s: %v", method, u, err)
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("zhihu: warmup %s %s: http %d", method, u, resp.StatusCode)
	}
	sess.captureCookies(resp.Header)
	return true
}

// ---- browser-parity login ceremony (port of zhihu-plus-plus QrLogin.kt) ----
//
// The zhihu web login endpoints (QR begin / scan_info, SMS, sign-in) are
// dispatched through a fetch-style wrapper that DIFFERS from the data APIs:
// they must NOT carry the x-zse-93/96 signature used on /api feeds (sending
// it makes the QR begin answer HTTP 400 code 1000 "请求错误"), and instead
// rely on a real _xsrf cookie (minted by GET /signin during warmup), the
// sec-ch-ua trio and x-requested-with: fetch. Only the scan_info poll adds
// x-zse-93.

// loginFlowHeaders returns the login-path header set. isPolling mirrors the
// scan_info poll headers (Accept */* + sec-fetch* + x-zse-93).
func loginFlowHeaders(referer string, isPolling bool) map[string]string {
	h := map[string]string{
		"Accept-Encoding":    "gzip, deflate, br",
		"Accept-Language":    "en-US,en;q=0.9",
		"User-Agent":         zhihuWebUA,
		"sec-ch-ua":          secChUa,
		"sec-ch-ua-mobile":   secChUaMobile,
		"sec-ch-ua-platform": secChUaPlatform,
		"Origin":             "https://www.zhihu.com",
		"x-requested-with":   "fetch",
		"Content-Type":       "application/json;charset=UTF-8",
	}
	if referer != "" {
		h["Referer"] = referer
	}
	if isPolling {
		h["Accept"] = "*/*"
		h["sec-fetch-dest"] = "empty"
		h["sec-fetch-mode"] = "cors"
		h["sec-fetch-site"] = "same-origin"
		h["x-zse-93"] = zse93
	} else {
		h["Accept"] = "application/json, text/plain, */*"
	}
	return h
}

// loginRequest runs one login-ceremony call (should NOT be used for data
// feeds) and folds any Set-Cookie into the shared session. The HTTP status is
// returned unclassified: callers interpret 2xx / 400 / 403 (risk control)
// themselves. The first call primes the login prerequisites: getRaw skips its
// warmup while the session is still anonymous (401 fast-path), so without this
// gate the login endpoints would POST with an empty _xsrf/captcha_session_v2
// and be answered HTTP 400 code 1000 "请求错误". If a caller needs fresh
// prerequisites (not the first-call / process-once values), it should call
// refreshLoginContext itself before loginRequest (qrBegin does this).
func loginRequest(method, u string, body []byte, referer string, isPolling bool) ([]byte, int, error) {
	warmupOnce.Do(warmup)
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, u, rdr)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range loginFlowHeaders(referer, isPolling) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Cookie", sess.loginCookieHeader())
	if xsrf := sess.getXsrf(); xsrf != "" {
		req.Header.Set("x-xsrftoken", xsrf)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	sess.captureCookies(resp.Header)
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}
