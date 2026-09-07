package bdtieba

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Auth status values reported by TiebaAuth.AuthStatus.
const (
	AuthStatusEmpty   = ""
	AuthStatusReady   = "ready"
	AuthStatusInvalid = "invalid"
)

const (
	tbsURL    = "https://tieba.baidu.com/dc/common/tbs"
	commitURL = "https://tieba.baidu.com/f/commit/post/add"
	deleteURL = "https://tieba.baidu.com/f/commit/post/delete"
)

// ErrRateLimited is returned when Baidu rejects a write for posting too fast
// into a forum (err_code 220034).
var ErrRateLimited = errors.New("bdtieba: rate limited (err_code=220034), slow down and retry later")

// authFileJSON is the persisted credential shape in ~/.config/fedlet/bdtieba-auth.json.
type authFileJSON struct {
	BDUSS    string `json:"bduss"`
	STOKEN   string `json:"stoken,omitempty"`
	UserName string `json:"user_name,omitempty"`
	Status   string `json:"status"`
}

// TiebaAuth performs authenticated write operations (add/delete threads and
// replies) using Baidu cookies. Authentication is a manual cookie: the user
// extracts BDUSS (optionally BDUSS:STOKEN) from their browser login and passes
// it to SetAuth or stores it in the auth file.
type TiebaAuth struct {
	mu     sync.Mutex
	bduss  string
	stoken string
	user   string
	status string
	hc     *http.Client
}

// NewTiebaAuth loads any previously stored credential from
// ~/.config/fedlet/bdtieba-auth.json.
func NewTiebaAuth() *TiebaAuth {
	a := &TiebaAuth{
		status: AuthStatusEmpty,
		hc:     hc,
	}
	a.loadAuth()
	return a
}

// SetAuth accepts a credential of the form "BDUSS" or "BDUSS:STOKEN",
// persists it to the auth file and verifies it against Tieba.
func (a *TiebaAuth) SetAuth(cred string) error {
	bduss, stoken, err := parseAuth(cred)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.bduss = bduss
	a.stoken = stoken
	a.user = ""
	a.mu.Unlock()
	a.saveAuth()

	if _, err := a.getTbs(); err != nil {
		return err
	}
	return nil
}

// AuthStatus reports the current credential state: empty/ready/invalid.
func (a *TiebaAuth) AuthStatus() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

// UserName returns the logged-in account name, if known from a successful verify.
func (a *TiebaAuth) UserName() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.user
}

// AddThread creates a new thread in forum kw. title must be 30 characters or
// fewer. It returns the new thread id.
func (a *TiebaAuth) AddThread(kw, title, content string) (int64, error) {
	if kw == "" {
		return 0, fmt.Errorf("bdtieba: empty forum name")
	}
	if title == "" {
		return 0, fmt.Errorf("bdtieba: empty thread title")
	}
	if len([]rune(title)) > 30 {
		return 0, fmt.Errorf("bdtieba: thread title too long (%d runes, max 30)", len([]rune(title)))
	}
	if content == "" {
		return 0, fmt.Errorf("bdtieba: empty thread content")
	}

	fid, err := a.getFid(kw)
	if err != nil {
		return 0, err
	}
	form := url.Values{
		"ie":        {"utf-8"},
		"kw":        {kw},
		"fid":       {strconv.Itoa(fid)},
		"rich_text": {"1"},
		"tbs":       {},
		"title":     {title},
		"content":   {content},
		"vcode_md5": {""},
		"__type__":  {"thread"},
	}
	resp, err := a.postCommit("/f/commit/post/add", form)
	if err != nil {
		return 0, fmt.Errorf("bdtieba: add thread in %q: %w", kw, err)
	}
	if resp.Data.Tid == 0 {
		return 0, fmt.Errorf("bdtieba: add thread in %q returned tid 0 (%s)", kw, strings.TrimSpace(resp.Error))
	}
	return resp.Data.Tid, nil
}

// AddPost replies to thread tid in forum kw. It returns the new post id.
func (a *TiebaAuth) AddPost(kw string, tid int64, content string) (int64, error) {
	if kw == "" {
		return 0, fmt.Errorf("bdtieba: empty forum name")
	}
	if tid <= 0 {
		return 0, fmt.Errorf("bdtieba: invalid thread id %d", tid)
	}
	if content == "" {
		return 0, fmt.Errorf("bdtieba: empty reply content")
	}

	fid, err := a.getFid(kw)
	if err != nil {
		return 0, err
	}
	form := url.Values{
		"ie":        {"utf-8"},
		"kw":        {kw},
		"fid":       {strconv.Itoa(fid)},
		"tid":       {strconv.FormatInt(tid, 10)},
		"content":   {content},
		"is_login":  {"1"},
		"rich_text": {"1"},
		"tbs":       {},
		"vcode_md5": {""},
		"anonymous": {"0"},
		"__type__":  {"reply"},
	}
	resp, err := a.postCommit("/f/commit/post/add", form)
	if err != nil {
		return 0, fmt.Errorf("bdtieba: reply to tid=%d in %q: %w", tid, kw, err)
	}
	if resp.Data.Pid == 0 {
		return 0, fmt.Errorf("bdtieba: reply to tid=%d in %q returned pid 0 (%s)", tid, kw, strings.TrimSpace(resp.Error))
	}
	return resp.Data.Pid, nil
}

// DelThread deletes one of the account's own threads. The first-floor post id
// equals the thread id, so only tid is needed.
func (a *TiebaAuth) DelThread(kw string, tid int64) error {
	if kw == "" {
		return fmt.Errorf("bdtieba: empty forum name")
	}
	if tid <= 0 {
		return fmt.Errorf("bdtieba: invalid thread id %d", tid)
	}
	form := url.Values{
		"kw":  {kw},
		"tid": {strconv.FormatInt(tid, 10)},
		"pid": {strconv.FormatInt(tid, 10)},
		"tbs": {},
	}
	if _, err := a.postCommit("/f/commit/post/delete", form); err != nil {
		return fmt.Errorf("bdtieba: delete thread tid=%d in %q: %w", tid, kw, err)
	}
	return nil
}

// DelPost deletes one of the account's own replies.
func (a *TiebaAuth) DelPost(kw string, tid, pid int64) error {
	if kw == "" {
		return fmt.Errorf("bdtieba: empty forum name")
	}
	if tid <= 0 {
		return fmt.Errorf("bdtieba: invalid thread id %d", tid)
	}
	if pid <= 0 {
		return fmt.Errorf("bdtieba: invalid post id %d", pid)
	}
	form := url.Values{
		"kw":  {kw},
		"tid": {strconv.FormatInt(tid, 10)},
		"pid": {strconv.FormatInt(pid, 10)},
		"tbs": {},
	}
	if _, err := a.postCommit("/f/commit/post/delete", form); err != nil {
		return fmt.Errorf("bdtieba: delete post pid=%d in tid=%d %q: %w", pid, tid, kw, err)
	}
	return nil
}

// UpdateThread rewrites an existing thread by deleting it and posting the new
// content, since Tieba offers no edit API. The old thread is deleted first; the
// returned id is the new thread. If the repost fails the old thread is already
// gone, and the returned error says so.
func (a *TiebaAuth) UpdateThread(kw, title, content string, oldTid int64) (int64, error) {
	if err := a.DelThread(kw, oldTid); err != nil {
		return 0, fmt.Errorf("bdtieba: update: delete old thread tid=%d: %w", oldTid, err)
	}
	ntid, err := a.AddThread(kw, title, content)
	if err != nil {
		return 0, fmt.Errorf("bdtieba: update: old thread tid=%d deleted but repost failed: %w", oldTid, err)
	}
	return ntid, nil
}

// getTbs fetches a fresh anti-CSRF token and verifies the stored cookie. It
// refreshes AuthStatus on every call.
func (a *TiebaAuth) getTbs() (string, error) {
	a.mu.Lock()
	cookie := a.cookieLocked()
	a.mu.Unlock()
	if cookie == "" {
		a.setStatus(AuthStatusEmpty)
		return "", fmt.Errorf("bdtieba: no BDUSS set (call SetAuth or fill %s)", authFilePath())
	}

	req, err := http.NewRequest(http.MethodGet, tbsURL, nil)
	if err != nil {
		return "", fmt.Errorf("bdtieba: build tbs request: %w", err)
	}
	req.Header.Set("User-Agent", mobileUA)
	req.Header.Set("Referer", "https://tieba.baidu.com/")
	req.Header.Set("Cookie", cookie)

	resp, err := a.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("bdtieba: get tbs: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("bdtieba: read tbs response: %w", err)
	}

	var t tbsResp
	if err := json.Unmarshal(body, &t); err != nil {
		return "", fmt.Errorf("bdtieba: parse tbs response %s: %w", truncate(string(body), 200), err)
	}
	if t.IsLogin != 1 {
		a.setStatus(AuthStatusInvalid)
		return "", fmt.Errorf("bdtieba: BDUSS invalid (is_login=0), re-extract the cookie from your browser")
	}
	a.mu.Lock()
	a.user = t.UserName
	a.mu.Unlock()
	a.setStatus(AuthStatusReady)
	return t.Tbs, nil
}

// getFid resolves the numeric forum id for kw, reusing the anonymous thread
// list lookup already implemented for polling.
func (a *TiebaAuth) getFid(kw string) (int, error) {
	data, err := FetchFrs(kw, 1, 1)
	if err != nil {
		return 0, fmt.Errorf("bdtieba: fid lookup for %q: %w", kw, err)
	}
	if data.Forum.ID == 0 {
		return 0, fmt.Errorf("bdtieba: fid lookup for %q returned an empty forum (does it exist?)", kw)
	}
	return data.Forum.ID, nil
}

// postCommit posts a form to a /f/commit/* endpoint with the auth cookie and a
// fresh tbs, and parses the Tieba JSON result.
func (a *TiebaAuth) postCommit(path string, form url.Values) (*commitResp, error) {
	tbs, err := a.getTbs()
	if err != nil {
		return nil, err
	}
	if form.Get("tbs") == "" {
		form.Set("tbs", tbs)
	}

	req, err := http.NewRequest(http.MethodPost, "https://tieba.baidu.com"+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("bdtieba: build commit request %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", mobileUA)
	req.Header.Set("Referer", "https://tieba.baidu.com/")
	a.mu.Lock()
	req.Header.Set("Cookie", a.cookieLocked())
	a.mu.Unlock()

	resp, err := a.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bdtieba: commit %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("bdtieba: read commit %s response: %w", path, err)
	}

	var cr commitResp
	if err := json.Unmarshal(body, &cr); err != nil {
		return nil, fmt.Errorf("bdtieba: parse commit %s response %s: %w", path, truncate(string(body), 200), err)
	}
	if cr.ErrCode != 0 {
		if cr.ErrCode == 220034 {
			return nil, ErrRateLimited
		}
		return nil, fmt.Errorf("bdtieba: commit %s failed err_code=%d %s", path, cr.ErrCode, strings.TrimSpace(cr.Error))
	}
	return &cr, nil
}

func (a *TiebaAuth) setStatus(s string) {
	a.mu.Lock()
	a.status = s
	a.mu.Unlock()
	if s == AuthStatusInvalid {
		a.saveAuth()
	}
}

func (a *TiebaAuth) cookieLocked() string {
	if a.stoken != "" {
		return "BDUSS=" + a.bduss + "; STOKEN=" + a.stoken
	}
	return "BDUSS=" + a.bduss
}

// parseAuth accepts "BDUSS" or "BDUSS:STOKEN".
func parseAuth(cred string) (bduss, stoken string, err error) {
	cred = strings.TrimSpace(cred)
	if cred == "" {
		return "", "", errors.New("bdtieba: empty credential")
	}
	if !strings.Contains(cred, ":") {
		return cred, "", nil
	}
	parts := strings.Split(cred, ":")
	if len(parts) != 2 || parts[0] == "" {
		return "", "", fmt.Errorf("bdtieba: expected BDUSS or BDUSS:STOKEN")
	}
	return parts[0], parts[1], nil
}

func authFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "fedlet", "bdtieba-auth.json")
}

func (a *TiebaAuth) loadAuth() {
	data, err := os.ReadFile(authFilePath())
	if err != nil {
		return
	}
	var f authFileJSON
	if err := json.Unmarshal(data, &f); err != nil {
		log.Printf("bdtieba: load auth parse error: %v", err)
		return
	}
	a.mu.Lock()
	a.bduss = f.BDUSS
	a.stoken = f.STOKEN
	a.user = f.UserName
	if f.BDUSS != "" && f.Status != AuthStatusInvalid {
		a.status = AuthStatusReady
	} else {
		a.status = f.Status
	}
	a.mu.Unlock()
}

func (a *TiebaAuth) saveAuth() {
	a.mu.Lock()
	f := authFileJSON{
		BDUSS:    a.bduss,
		STOKEN:   a.stoken,
		UserName: a.user,
		Status:   a.status,
	}
	a.mu.Unlock()
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		log.Printf("bdtieba: save auth marshal error: %v", err)
		return
	}
	p := authFilePath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		log.Printf("bdtieba: save auth mkdir error: %v", err)
		return
	}
	if err := os.WriteFile(p, data, 0600); err != nil {
		log.Printf("bdtieba: save auth write error: %v", err)
	}
}

// tbsResp is the response of /dc/common/tbs.
type tbsResp struct {
	Tbs      string `json:"tbs"`
	IsLogin  int    `json:"is_login"`
	UserName string `json:"user_name"`
}

// commitResp is the common envelope of /f/commit/* endpoints.
type commitResp struct {
	No      int    `json:"no"`
	ErrCode int    `json:"err_code"`
	Error   string `json:"error"`
	Data    struct {
		Tid int64 `json:"tid"`
		Pid int64 `json:"pid"`
	} `json:"data"`
}
