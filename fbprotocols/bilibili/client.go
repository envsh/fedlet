package bilibili

// HTTP client core for the bilibili web protocol.
//
// A single shared cookie jar (buvid3 / SESSDATA / bili_jct / DedeUserID /
// bili_ticket ...) backs every endpoint. The visitor cookies buvid3 + b_nut
// are minted via /x/frontend/finger/spi (best-effort warmup), and a bili_ticket
// is fetched for the /wbi/ recommend feed when the environment allows.
//
// Error classification contract (mirrors the zhihu/xhs feed gateways):
//   - JSON code -101 / HTTP 401                -> ErrNotLoggedIn (session dead)
//   - JSON code -352 / HTTP 412                -> ErrRiskControl (backoff, keep session)
//   - JSON code -403 with v_voucher            -> ErrRiskControl (human gaia captcha)
//   - JSON code -400/-100/-799 other           -> plain error (param, no auth)
//   - HTTP 200 + HTML body                     -> errUnverifiable (shed)
//   - HTTP 522/404 + HTML                      -> errUnverifiable (anti-bot node)
//   - network / timeout / 5xx                  -> transient error (keep session)
//
// None of the classification paths mutate the session; the auth.go gateway
// decides whether the session is actually dead.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ErrNotLoggedIn is returned when an authenticated endpoint rejects the stored
// session (code -101 / HTTP 401). It triggers the expired-session handler but
// is not a network or rate-limit failure.
var ErrNotLoggedIn = errors.New("bilibili: not logged in or session expired")

// ErrRiskControl marks account/network-level blocks (-352 / 412 / 403 v_voucher)
// that are throttled and mostly need human verification; never a session death.
var ErrRiskControl = errors.New("bilibili: blocked by risk control")

// errUnverifiable marks a 200 (or 404/522 anti-bot) response carrying an HTML
// page instead of the expected JSON envelope. Classified by errors.Is at the
// session probe and the feed error router.
var errUnverifiable = errors.New("bilibili: unverifiable html response")

const (
	biliUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"

	passportHost = "https://passport.bilibili.com"
	apiHost      = "https://api.bilibili.com"
	webHost      = "https://www.bilibili.com"

	navURL    = apiHost + "/x/web-interface/nav"
	fingerURL = apiHost + "/x/frontend/finger/spi"
	ticketURL = apiHost + "/bapis/bilibili.api.ticket.v1.Ticket/GenWebTicket"
)

// cookieStore is a small mutex-guarded cookie jar (domain is irrelevant: every
// endpoint shares the same cookie header, matching the web client behaviour).
type cookieStore struct {
	mu sync.RWMutex
	m  map[string]string
}

func newCookieStore() *cookieStore { return &cookieStore{m: make(map[string]string)} }

func (c *cookieStore) set(k, v string) {
	if v == "" {
		return
	}
	c.mu.Lock()
	c.m[k] = v
	c.mu.Unlock()
}

func (c *cookieStore) get(k string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.m[k]
}

func (c *cookieStore) all() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]string, len(c.m))
	for k, v := range c.m {
		out[k] = v
	}
	return out
}

func (c *cookieStore) captureSetCookie(h http.Header) {
	for _, sc := range h.Values("Set-Cookie") {
		name, rest, ok := strings.Cut(sc, "=")
		if !ok {
			continue
		}
		value, _, _ := strings.Cut(rest, ";")
		value = strings.TrimSpace(value)
		if name != "" && value != "" {
			c.set(name, value)
		}
	}
}

func (c *cookieStore) header() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var parts []string
	for k, v := range c.m {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

// jar is the shared cookie jar for the whole protocol.
var jar = newCookieStore()

// biliClient is the stateless HTTP client the endpoints use (cookie jar is
// global, matching the zhihu/xhs single-session model).
var hc = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	},
}

// envResp is the {code,message,data} envelope bilibili JSON APIs use.
type envResp struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// doBili performs one request against an api.bilibili.com endpoint, injecting
// the browser headers + the shared cookie jar, and returns the raw body, the
// HTTP status and a classified error.
func doBili(method, u string, form url.Values) ([]byte, int, error) {
	var rdr io.Reader
	if form != nil {
		rdr = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, u, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", biliUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("Origin", webHost)
	req.Header.Set("Cookie", jar.header())
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	jar.captureSetCookie(resp.Header)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		if looksHTML(body) {
			noteRateLimit()
			return body, resp.StatusCode, fmt.Errorf("%w http 200 %s", errUnverifiable, u)
		}
	case http.StatusUnauthorized:
		return body, resp.StatusCode, ErrNotLoggedIn
	case http.StatusForbidden:
		return body, resp.StatusCode, fmt.Errorf("bilibili: http 403 %s", truncate(string(body), 160))
	case http.StatusTooManyRequests, http.StatusPreconditionFailed:
		return body, resp.StatusCode, ErrRiskControl
	case http.StatusNotFound:
		// Anti-bot 404 nodes often serve an HTML page for a valid path; treat
		// as unverifiable rather than a hard miss.
		if looksHTML(body) {
			return body, resp.StatusCode, fmt.Errorf("%w http 404 %s", errUnverifiable, u)
		}
	case 522:
		return body, resp.StatusCode, fmt.Errorf("%w http 522 %s", errUnverifiable, u)
	}
	return body, resp.StatusCode, nil
}

// getJSON decodes a {code:0,data} envelope. needAuth gates the request on the
// SESSDATA cookie; non-zero codes are classified according to the error table.
func getJSON(data any, u string, needAuth bool) error {
	body, status, err := doBili(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("bilibili: http %d %s", status, truncate(string(body), 160))
	}
	var env envResp
	if err := json.Unmarshal(body, &env); err != nil {
		if looksHTML(body) {
			return fmt.Errorf("%w %s", errUnverifiable, u)
		}
		return fmt.Errorf("bilibili: parse %s: %w", u, err)
	}
	if env.Code != 0 {
		return classifyAPIError(u, env.Code, env.Message)
	}
	if needAuth && jar.get("SESSDATA") == "" {
		return ErrNotLoggedIn
	}
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, data); err != nil {
			return fmt.Errorf("bilibili: parse data %s: %w", u, err)
		}
	}
	return nil
}

// postForm posts a url-encoded form and decodes the envelope, classifying the
// API code the same way as getJSON.
func postForm(data any, u string, form url.Values) error {
	body, status, err := doBili(http.MethodPost, u, form)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("bilibili: http %d %s", status, truncate(string(body), 160))
	}
	var env envResp
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("bilibili: parse %s: %w", u, err)
	}
	if env.Code != 0 {
		return classifyAPIError(u, env.Code, env.Message)
	}
	if len(env.Data) > 0 && data != nil {
		if err := json.Unmarshal(env.Data, data); err != nil {
			return fmt.Errorf("bilibili: parse data %s: %w", u, err)
		}
	}
	return nil
}

// classifyAPIError maps an api.bilibili.com JSON code onto the shared error
// taxonomy; anything unlisted falls through as a plain error (no auth signal).
func classifyAPIError(u string, code int, msg string) error {
	switch code {
	case -101, -401:
		return ErrNotLoggedIn
	case -352, -412:
		return ErrRiskControl
	case -403:
		return ErrRiskControl
	default:
		if msg == "" {
			msg = "api error"
		}
		return fmt.Errorf("bilibili: %s code %d: %s", u, code, msg)
	}
}

// ---- anonymous visitor warmup (best-effort, non-fatal) ----

var warmupOnce sync.Once

// warmup mints the visitor cookies once: /x/frontend/finger/spi gives
// buvid3/b_nut (the two anonymous id cookies every endpoint expects), and
// /nav populates the WBI keys for the signed recommend feed. It also pings
// GET www.bilibili.com to let the CDN seed the domain cookies.
func warmup() {
	if err := getJSON(nil, fingerURL, false); err != nil {
		log.Printf("bilibili: warmup finger error: %v", err)
	}
	nav := map[string]any{}
	if err := getJSONDataRaw(&nav, navURL); err != nil {
		log.Printf("bilibili: warmup nav error: %v", err)
	}
	wbiKeysFromNav(nav)
	biliTicket()
	log.Printf("bilibili: warmup done cookies=%s wbi=%v", jarState(), wbiReady())
}

// getJSONDataRaw is getJSON without a typed target (used by warmup for nav).
func getJSONDataRaw(data any, u string) error {
	body, status, err := doBili(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("bilibili: http %d", status)
	}
	return json.Unmarshal(body, data)
}

// biliTicket best-effort fetches the bili_ticket cookie that some /wbi/
// endpoints (video recommend) gate on. The GenWebTicket call needs a csrf
// (bili_jct) and a nav_id; without them it may answer -101 or a risk block,
// which is fine — the recommend feed degrades gracefully.
func biliTicket() {
	form := url.Values{}
	if csrf := jar.get("bili_jct"); csrf != "" {
		form.Set("csrf", csrf)
	}
	form.Set("platform", "web")
	form.Set("ts", fmt.Sprintf("%d", time.Now().Unix()))
	var data struct {
		Ticket string `json:"ticket"`
		NavID  int    `json:"nav_id"`
	}
	if err := postForm(&data, ticketURL, form); err != nil {
		log.Printf("bilibili: bili_ticket best-effort failed: %v", err)
		return
	}
	if data.Ticket != "" {
		jar.set("bili_ticket", data.Ticket)
	}
}

// ensureWarmup runs the one-time visitor bootstrap before any network call that
// actually reaches the server.
func ensureWarmup() { warmupOnce.Do(warmup) }

func wbiReady() bool {
	wbiMu.Lock()
	defer wbiMu.Unlock()
	return wbiKeysData.mixinKey != ""
}

func jarState() string {
	return strings.Join([]string{
		"buvid3=" + y("buvid3"),
		"SESSDATA=" + y("SESSDATA"),
		"bili_jct=" + y("bili_jct"),
		"bili_ticket=" + y("bili_ticket"),
	}, " ")
}

func y(k string) string {
	if jar.get(k) != "" {
		return "y"
	}
	return "n"
}

// ---- small helpers ----

func looksHTML(b []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(b), []byte{'<'})
}

var truncateRE = regexp.MustCompile(`\s+`)

func truncate(s string, n int) string {
	s = truncateRE.ReplaceAllString(strings.TrimSpace(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
