package bdtieba

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"
)

const (
	frsBaseURL = "https://tieba.baidu.com/mg/f/getFrsData"
	mobileUA   = "Mozilla/5.0 (iPhone; CPU iPhone OS 16_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Mobile/15E148 Safari/604.1"
)

// hc is the package-wide HTTP client shared by all requests. It keeps the
// session cookies set by Baidu (IS_NEW_USER/TIEBAUID/BAIDUID) in an in-memory
// jar and reuses persistent HTTP/2 connections, so each getFrsData/getPbData
// call presents a consistent anonymous session instead of a fresh one.
var hc = func() *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic("bdtieba: cookiejar: " + err.Error())
	}
	return &http.Client{
		Timeout: 15 * time.Second,
		Jar:     jar,
		Transport: &http.Transport{
			ForceAttemptHTTP2:   true,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}
}()

// getRaw is the shared request path: it blocks on the rate-limit gate, builds
// the anonymous mobile-API request, performs it via hc (reusing the session
// cookie jar and HTTP/2 connections), triggers backoff on a bfe 403 and resets
// it on success. It returns the raw body and status code.
func getRaw(u, referer string) ([]byte, int, error) {
	waitRateGate()

	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", mobileUA)
	req.Header.Set("Referer", referer)
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}

	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("Server") == "bfe" {
		noteRateLimit()
	} else if resp.StatusCode == http.StatusOK {
		clearRateLimit()
	}
	return body, resp.StatusCode, nil
}

// FetchFrs fetches page pn of the thread list of forum kw.
// rn is the requested number of items per request (default 30).
//
// The endpoint accepts a sort_type query parameter but FetchFrs intentionally
// does NOT send it, so Tieba applies its default ordering (equivalent to
// sort_type=0): pin threads first, then by last-reply time (last_time_int
// descending). Measured sort_type values (2026-09, getFrsData):
//
//	0  default: pin first, then last-reply time desc
//	1  create/publish time desc (newest threads first); for a "new thread
//	   stream" ordering, pass &sort_type=1 explicitly
//	2  identical to 0 (no distinct meaning)
//	3  non-JSON/error response (unusable)
func FetchFrs(kw string, pn, rn int) (*FrsData, error) {
	if pn <= 0 {
		pn = 1
	}
	if rn <= 0 {
		rn = 30
	}
	u := fmt.Sprintf("%s?kw=%s&rn=%d&pn=%d", frsBaseURL, url.QueryEscape(kw), rn, pn)

	body, status, err := getRaw(u, "https://tieba.baidu.com/")
	if err != nil {
		return nil, fmt.Errorf("bdtieba: get %q: %w", kw, err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("bdtieba: %q http %d %s", kw, status, truncate(string(body), 200))
	}

	var out FrsResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("bdtieba: parse %q: %w", kw, err)
	}
	if out.ErrorCode != 0 {
		return nil, fmt.Errorf("bdtieba: %q error_code=%d %s", kw, out.ErrorCode, out.ErrorMsg)
	}
	return &out.Data, nil
}
