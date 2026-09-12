package xhs

// HTTP client core for the xhs web protocol: signed header generation,
// cookie handling, and the anti-bot (461/471/472) retry interceptor.
//
// Signing matches xhshow 0.2.0 (XYS_/XYW_ + x-s-common). Structural rules
// observed from the production client are preserved: the Cookie header always
// sits in a fixed position, x-t/x-b3 follow, x-s/x-s-common come next, and
// xy-direction closes the list.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	xhsBaseURL = "https://www.xiaohongshu.com"
	xhsAPIHost = "https://edith.xiaohongshu.com"

	// xhsWebSessionName is the local auth cookie written on successful login.
	xhsWebSessionName = "WebSession"
)

// buildJSONBody serializes WITHOUT HTML escaping and keeps insertion order
// (uses the ordered []kvParam). Matches json.dumps(ensure_ascii=False,
// separators=(",",":")).
func buildJSONBody(params []kvParam) string {
	var sb strings.Builder
	sb.WriteByte('{')
	for i, p := range params {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteByte('"')
		sb.WriteString(jsonEscape(p.key))
		sb.WriteString(`":`)
		sb.WriteString(marshalJSONScalar(p.pValue()))
	}
	sb.WriteByte('}')
	return sb.String()
}

// buildURL mirrors Python build_url(): adds query params and percent-encodes
// only the parts that need it (values get %3D for '=', spaces %20).
func buildURL(baseURL string, params []kvParam) string {
	if len(params) == 0 {
		return baseURL
	}
	q := make(url.Values)
	keys := make([]string, 0, len(params))
	seen := map[string]bool{}
	for _, p := range params {
		if !seen[p.key] {
			seen[p.key] = true
			keys = append(keys, p.key)
		}
		q.Add(p.key, p.pValue())
	}
	sep := "?"
	if strings.Contains(baseURL, "?") {
		sep = "&"
	}
	return baseURL + sep + q.Encode()
}

func jsonEscape(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func marshalJSONScalar(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	out := buf.Bytes()
	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	return string(out)
}

// --- cookies ---

// cookieStore is a small mutex-guarded cookie jar shared by every request,
// the login UI and the risk handler.
type cookieStore struct {
	mu sync.RWMutex
	m  map[string]string
}

func newCookieStore() *cookieStore { return &cookieStore{m: make(map[string]string)} }

func (c *cookieStore) set(k, v string) {
	c.mu.Lock()
	c.m[k] = v
	c.mu.Unlock()
}
func (c *cookieStore) get(k string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.m[k]
}
func (c *cookieStore) has(k string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.m[k]
	return ok
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

// parseCookieMap extracts name=value pairs from a raw Cookie header value.
func parseCookieMap(raw string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		i := strings.IndexByte(part, '=')
		if i <= 0 {
			continue
		}
		if v, err := url.QueryUnescape(part[i+1:]); err == nil {
			out[part[:i]] = v
		} else {
			out[part[:i]] = part[i+1:]
		}
	}
	return out
}

// captureCookies folds Set-Cookie response headers into the jar. Non-empty
// cookies only; malformed entries are skipped. This is how login exchanges
// (websectiga, sec_poison_id, web_session, ...) reach the shared jar.
func (c *cookieStore) captureCookies(h http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sc := range h.Values("Set-Cookie") {
		name, rest, ok := strings.Cut(sc, "=")
		if !ok {
			continue
		}
		value, _, _ := strings.Cut(rest, ";")
		value, _ = url.QueryUnescape(strings.TrimSpace(value))
		if name != "" && value != "" {
			c.m[name] = value
		}
	}
}

// load replaces the whole jar under one lock.
func (c *cookieStore) load(m map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = make(map[string]string, len(m))
	for k, v := range m {
		if v != "" {
			c.m[k] = v
		}
	}
}

// del removes one cookie from the jar (used to drop a dead web_session).
func (c *cookieStore) del(k string) {
	c.mu.Lock()
	delete(c.m, k)
	c.mu.Unlock()
}

// signedHeaders are the per-request signature headers.
type signedHeaders struct {
	order []string
	vals  map[string]string
}

func (h *signedHeaders) add(k, v string) {
	if h.vals == nil {
		h.vals = map[string]string{}
	}
	h.order = append(h.order, k)
	h.vals[k] = v
}

func (h *signedHeaders) render() []string {
	out := make([]string, 0, len(h.order))
	for _, k := range h.order {
		out = append(out, k+": "+h.vals[k])
	}
	return out
}

type signHeadersOptions struct {
	method    string // "GET" or "POST"
	uri       string // path only ("/api/sns/web/...") or full URL
	a1        string
	cookieRaw string // full cookie string used for x-s-common + Cookie header
	appID     string // defaults to "xhs-pc-web"
	params    []kvParam
	body      string // pre-serialized compact JSON for POST
	ts        float64
	format    string // "xys" (default) or "xyw"
	userID    string
	withXRap  bool
}

// buildSignedHeaders produces the full header set for one request.
func buildSignedHeaders(o signHeadersOptions) (*signedHeaders, error) {
	if o.a1 == "" {
		return nil, fmt.Errorf("xhs: missing a1 cookie")
	}
	if o.format == "" {
		o.format = "xys"
	}
	if o.appID == "" {
		o.appID = "xhs-pc-web"
	}
	uri := extractURI(o.uri)

	s := newSigner()
	var xS string
	var err error
	if o.format == "xyw" {
		xS, err = s.signXYW(o.method, uri, o.a1, o.appID, o.body, o.params, o.ts)
	} else {
		xS, err = s.signXS(o.method, uri, o.a1, o.appID, o.body, o.params, o.ts)
	}
	if err != nil {
		return nil, err
	}
	xSC, err := s.signXSCommon(parseCookieMap(o.cookieRaw))
	if err != nil {
		return nil, err
	}

	h := &signedHeaders{}
	h.add("x-s", xS)
	h.add("x-s-common", xSC)
	h.add("x-t", fmt.Sprintf("%d", int64(o.ts*1000)))
	h.add("x-b3-traceid", b3TraceID())
	h.add("x-xray-traceid", xrayTraceID(time.Now()))
	h.add("x-mns", "unload")
	h.add("xy-direction", fmt.Sprintf("%d", shardingKey(o.userID)))

	if o.withXRap {
		rap, err := xrapParam(xrapOptions{
			api:  "//edith.xiaohongshu.com" + uri,
			body: o.body,
		})
		if err != nil {
			return nil, err
		}
		h.add("x-rap-param", rap)
	}
	return h, nil
}

// --- request client ---

// riskSignal describes a blocked response (461/471/472); headers carry the
// verify captcha coordinates (verifytype/verifyuuid) read by captcha.go.
type riskSignal struct {
	status  int
	body    []byte
	headers http.Header
}

// riskResolver is invoked when the API returns a risk challenge; it is wired
// by xhs.go to the captcha/login flow (captcha.go, loginsrv.go).
type riskResolver func(sig riskSignal, cookies map[string]string) (ok bool, newCookies map[string]string)

// authState tracks the login status/user strings; it is a pointer so copying
// clientOptions never copies the mutex.
type authState struct {
	mu          sync.Mutex
	user        string
	status      string
	loginMethod string
}

type clientOptions struct {
	timeout time.Duration
	onRisk  riskResolver
	userID  string
	auth    *authState
}

type xhsClient struct {
	http    *http.Client
	opts    clientOptions
	cookies *cookieStore
}

func newXHSClient(opts clientOptions) *xhsClient {
	if opts.timeout == 0 {
		opts.timeout = 30 * time.Second
	}
	if opts.auth == nil {
		opts.auth = &authState{}
	}
	c := &xhsClient{
		http:    &http.Client{Timeout: opts.timeout},
		opts:    opts,
		cookies: newCookieStore(),
	}
	a1 := generateA1()
	c.cookies.set("a1", a1)
	c.cookies.set("webId", webIDFromA1(a1))
	return c
}

func (c *xhsClient) cookieHeader() string {
	sb := strings.Builder{}
	c.cookies.mu.RLock()
	keys := make([]string, 0, len(c.cookies.m))
	for k := range c.cookies.m {
		keys = append(keys, k)
	}
	c.cookies.mu.RUnlock()
	sort.Strings(keys)
	c.cookies.mu.RLock()
	for i, k := range keys {
		if i > 0 {
			sb.WriteString("; ")
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(c.cookies.m[k])
	}
	c.cookies.mu.RUnlock()
	return sb.String()
}

// doSigned runs one signed request with the fixed header order, then lets the
// caller decide on retries. cookieRaw is always the live jar so x-s-common and
// the Cookie header agree.
func (c *xhsClient) doSigned(o signHeadersOptions) (*http.Response, []byte, *signedHeaders, error) {
	o.a1 = c.cookies.get("a1")
	o.cookieRaw = c.cookieHeader()
	if o.appID == "" {
		o.appID = "xhs-pc-web"
	}
	h, err := buildSignedHeaders(o)
	if err != nil {
		return nil, nil, nil, err
	}

	full := buildURL(xhsAPIHost+extractURI(o.uri), o.params)
	var body io.Reader
	if o.method == "POST" {
		body = strings.NewReader(o.body)
	} else {
		body = nil
	}
	req, err := http.NewRequest(o.method, full, body)
	if err != nil {
		return nil, nil, nil, err
	}

	req.Header.Set("Cookie", o.cookieRaw)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", xhsBaseURL)
	req.Header.Set("Referer", xhsBaseURL+"/")
	req.Header.Set("User-Agent", xhsUA)
	for _, kv := range h.render() {
		k, v, _ := strings.Cut(kv, ": ")
		req.Header.Set(k, v)
	}
	if o.method == "POST" {
		tstamp := int64(o.ts * 1000)
		req.Header.Set("x-org-version", fmt.Sprintf("2026-03-20-%d", tstamp%100000))
		req.Header.Set("x-client-via", fmt.Sprintf("feigen:%d", (tstamp/1000)%100000))
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, nil, err
	}
	c.cookies.captureCookies(resp.Header)
	return resp, data, h, nil
}

// exec executes a signed request, auto-retrying once through the risk
// resolver when the API blocks with a captcha challenge.
func (c *xhsClient) exec(o signHeadersOptions) ([]byte, int, error) {
	resp, data, _, err := c.doSigned(o)
	if err != nil {
		return nil, 0, err
	}
	status := resp.StatusCode
	if status != http.StatusOK && c.isRiskStatus(status) {
		newJar, ok := c.resolveRisk(status, data, resp)
		if !ok {
			return data, status, nil
		}
		if len(newJar) > 0 {
			c.cookies.load(newJar)
			c.syncFromJar()
		}
		resp2, data2, _, err := c.doSigned(o)
		if err != nil {
			return nil, 0, err
		}
		return data2, resp2.StatusCode, nil
	}
	return data, status, nil
}

func (c *xhsClient) isRiskStatus(code int) bool {
	return code == 461 || code == 471 || code == 472
}

// resolveRisk hands off a captcha response to the wired resolver. If the
// resolver says "handled externally, keep this response", ok=false is the
// caller's signal to hand the challenge to the interactive login page.
func (c *xhsClient) resolveRisk(status int, body []byte, resp *http.Response) (map[string]string, bool) {
	if c.opts.onRisk == nil {
		return nil, false
	}
	jar := c.cookies.all()
	ok, newJar := c.opts.onRisk(riskSignal{status: status, body: body, headers: resp.Header.Clone()}, jar)
	if !ok {
		return nil, false
	}
	return newJar, true
}

// syncFromJar reloads a1/webId from the current jar, keeping the signing
// cookies aligned after a persisted jar replaces the in-memory one.
func (c *xhsClient) syncFromJar() {
	a1 := c.cookies.get("a1")
	if a1 == "" {
		a1 = generateA1()
		c.cookies.set("a1", a1)
	}
	if !c.cookies.has("webId") {
		c.cookies.set("webId", webIDFromA1(a1))
	}
}

func (c *xhsClient) getJSON(uri string, params []kvParam) (map[string]any, int, error) {
	data, status, err := c.exec(signHeadersOptions{method: "GET", uri: uri, params: params, ts: float64AtNow()})
	if err != nil {
		return nil, status, err
	}
	out, status, err := decodeXHSPayload(data, status)
	if err != nil {
		return nil, status, err
	}
	return out, status, nil
}

func (c *xhsClient) postJSON(uri string, params []kvParam) (map[string]any, int, error) {
	body := buildJSONBody(params)
	data, status, err := c.exec(signHeadersOptions{method: "POST", uri: uri, body: body, ts: float64AtNow()})
	if err != nil {
		return nil, status, err
	}
	out, status, err := decodeXHSPayload(data, status)
	if err != nil {
		return nil, status, err
	}
	return out, status, nil
}

func float64AtNow() float64 {
	now := time.Now()
	return float64(now.UnixMilli()) / 1000
}

// decodeXHSPayload unwraps {"code":0,"data":...} envelopes and surfaces
// non-zero codes and non-200 statuses so callers get the server answer
// without extra plumbing. The risk statuses (461/471/472) short-circuit with
// a typed error the orchestrator uses to decide retry/captcha.
func decodeXHSPayload(raw []byte, status int) (map[string]any, int, error) {
	if status != http.StatusOK {
		return nil, status, fmt.Errorf("xhs: http %d %s", status, truncate(string(raw), 200))
	}
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		if looksHTML(raw) {
			// A verification/risk-control page instead of JSON: back off rather
			// than hammer the endpoint every poll tick.
			noteRateLimit()
		}
		return nil, status, fmt.Errorf("xhs: bad json: %w", err)
	}
	if code, _ := env["code"].(float64); code != 0 {
		msg, _ := env["msg"].(string)
		return nil, status, fmt.Errorf("xhs: api code %v: %s", env["code"], msg)
	}
	if data, ok := env["data"]; ok {
		if m, ok := data.(map[string]any); ok {
			return m, status, nil
		}
		env["__data"] = data
	}
	return env, status, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// looksHTML reports whether a body starts like an HTML page (verification /
// risk-control) instead of the JSON envelope the API normally returns.
func looksHTML(b []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(b), []byte{'<'})
}

// xhsUA mirrors the PC web client runtime.
const xhsUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
