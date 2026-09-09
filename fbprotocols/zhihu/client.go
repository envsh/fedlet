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
	zhihuWebUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	apiReferer = "https://www.zhihu.com/hot"
)

// ErrNotLoggedIn is returned when an authenticated endpoint rejects the stored
// session (HTTP 401 / "未登录"). It triggers the expired-session handler but
// is not a network or rate-limit failure.
var ErrNotLoggedIn = errors.New("zhihu: not logged in or session expired")

// hc is the package-wide HTTP client. It keeps no cookie jar: cookies are
// injected explicitly per request from webSession so the d_c0 used in the
// signature always matches the one sent to the server.
var hc = func() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			ForceAttemptHTTP2:   true,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}
}()

const dc0Alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// webSession holds the cookies shared by all web requests: d_c0 (visitor
// token, present anonymous and feeding the signature) plus the login session
// z_c0 / _xsrf when authenticated.
type webSession struct {
	mu      sync.RWMutex
	dC0     string
	zC0     string
	xsrf    string
	zap     string
	qC1     string
	capsion string
	user    string
	status  string
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
	return b.String()
}

func (s *webSession) signDC0() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dC0
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
			}
			s.mu.Unlock()
		case "capsion_ticket":
			s.mu.Lock()
			if value != "" {
				s.capsion = value
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
		clearRateLimit()
	case http.StatusUnauthorized:
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

// postJSON is the POST equivalent of getJSON; it signs like every other API
// call and requires HTTP 200 + a JSON body.
func postJSON(data any, u string, payload []byte, needAuth bool) error {
	body, status, err := getRaw(http.MethodPost, u, payload, needAuth)
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
// call so the visitor cookies match the browser flow: the front page issues
// real _zap/_xsrf, POST /udid the real q_c1/d_c0, and the captcha endpoint a
// capsion_ticket (the login flows require these; code 101 AuthenticationError
// appears otherwise). Any step may fail gracefully — the fallback generated
// q_c1/d_c0 still satisfy most endpoints.
func warmup() {
	req, err := http.NewRequest(http.MethodGet, "https://www.zhihu.com/", nil)
	if err != nil {
		log.Printf("zhihu: warmup build request: %v", err)
		return
	}
	req.Header.Set("User-Agent", zhihuWebUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := hc.Do(req)
	if err != nil {
		log.Printf("zhihu: warmup get front page: %v", err)
		return
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	sess.captureCookies(resp.Header)
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
	// The captcha endpoint issues capsion_ticket, required by the QR/phone
	// login flows (and harmless to have for plain polling).
	warmupGet("https://www.zhihu.com/api/v3/oauth/captcha?lang=cn")

	sess.mu.Lock()
	if sess.qC1 == "" {
		sess.qC1 = newQC1()
	}
	sess.mu.Unlock()

	dc0 := sess.signDC0()
	short := dc0
	if len(short) > 30 {
		short = short[:30] + "..."
	}
	log.Printf("zhihu: warmup ok d_c0=%s api_version=%q", short, apiVersion)
}

// warmupGet/warmupPost run bare non-signed warmup requests (they must not go
// through getRaw, which re-enters warmup via sync.Once).
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
	req.Header.Set("User-Agent", zhihuWebUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", "https://www.zhihu.com/")
	req.Header.Set("Cookie", sess.cookieHeader())
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	sess.captureCookies(resp.Header)
	return true
}

// rawResp is a captured POST response (login flow) whose Set-Cookie headers
// have already been folded into the shared session by getRaw.
type rawResp struct {
	Body       []byte
	StatusCode int
}

func postRawCapture(u string, payload ...[]byte) (*rawResp, error) {
	var b []byte
	if len(payload) > 0 {
		b = payload[0]
	}
	body, status, err := getRaw(http.MethodPost, u, b, false)
	if err != nil {
		return nil, err
	}
	return &rawResp{Body: body, StatusCode: status}, nil
}
