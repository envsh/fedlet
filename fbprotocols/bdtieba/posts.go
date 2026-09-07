package bdtieba

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const pbBaseURL = "https://tieba.baidu.com/mg/p/getPbData"

const postDedupeExpiry = 72 * time.Hour

const (
	postPageSize = 30
	postJumpPage = 1 << 30
)

// flnum is a flexible numeric type that accepts both plain JSON numbers and
// numeric strings such as the getPbData endpoint's "errno":"0".
type flnum string

// UnmarshalJSON strips surrounding quotes if present.
func (n *flnum) UnmarshalJSON(b []byte) error {
	*n = flnum(strings.Trim(string(b), `"`))
	return nil
}

// PbResp is the response envelope of the getPbData endpoint.
type PbResp struct {
	Errno  flnum  `json:"errno"`
	ErrMsg string `json:"errmsg"`
	Data   PbData `json:"data"`
}

// PbData is the body of a getPbData response.
type PbData struct {
	Forum    Forum     `json:"forum"`
	Thread   *PbThread `json:"thread,omitempty"`
	PostList []Post    `json:"post_list"`
	Page     PbPage    `json:"page"`
}

// PbThread is the thread summary embedded in a getPbData response.
type PbThread struct {
	ID    int64  `json:"id"`
	Title string `json:"title,omitempty"`
}

// Post is one floor (楼层) of a thread.
type Post struct {
	ID       int64             `json:"id"`
	Floor    int               `json:"floor"`
	Title    string            `json:"title,omitempty"`
	Author   *Author           `json:"author,omitempty"`
	Content  []ContentFragment `json:"content"`
	Time     int64             `json:"time"`
	ReplyNum int64             `json:"reply_num"`
	SubPosts []SubPost         `json:"sub_post_list,omitempty"`
}

// SubPost is a reply inside a floor (楼中楼).
type SubPost struct {
	ID      int64             `json:"id"`
	Author  *Author           `json:"author,omitempty"`
	Content []ContentFragment `json:"content"`
	Time    int64             `json:"time"`
}

// ContentFragment is a single content chunk of a post: text ("text") or an
// image/emoji ("src").
type ContentFragment struct {
	Type int    `json:"type"`
	Text string `json:"text,omitempty"`
	Src  string `json:"src,omitempty"`
}

// PbPage holds pagination info of a getPbData response.
type PbPage struct {
	CurrentPage int `json:"current_page"`
	HasMore     int `json:"has_more"`
	PageSize    int `json:"page_size"`
	Offset      int `json:"offset"`
	TotalPage   int `json:"total_page"`
}

// FetchPosts fetches page pn of the floor list of thread tid.
// rn is the number of floors per request (default 5). The endpoint is
// anonymous and needs no authentication. Pagination is left to the caller via
// PbData.Page.HasMore.
func FetchPosts(tid int64, pn, rn int) (*PbData, error) {
	if tid <= 0 {
		return nil, fmt.Errorf("bdtieba: invalid tid %d", tid)
	}
	if pn <= 0 {
		pn = 1
	}
	if rn <= 0 {
		rn = 5
	}
	u := fmt.Sprintf("%s?kz=%d&pn=%d&rn=%d", pbBaseURL, tid, pn, rn)

	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("bdtieba: build request tid=%d: %w", tid, err)
	}
	req.Header.Set("User-Agent", mobileUA)
	req.Header.Set("Referer", fmt.Sprintf("https://tieba.baidu.com/p/%d", tid))
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bdtieba: get tid=%d: %w", tid, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("bdtieba: read tid=%d: %w", tid, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bdtieba: tid=%d http %d %s", tid, resp.StatusCode, truncate(string(body), 200))
	}

	var out PbResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("bdtieba: parse tid=%d: %w", tid, err)
	}
	if string(out.Errno) != "0" {
		return nil, fmt.Errorf("bdtieba: tid=%d errno=%s %s", tid, out.Errno, out.ErrMsg)
	}
	return &out.Data, nil
}

var (
	pstateMu sync.Mutex
	pstate   = stateData{}
)

func pstatePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "fedlet", "bdtieba-posts-state.json")
}

func loadPstate() {
	data, err := os.ReadFile(pstatePath())
	if err != nil {
		return
	}
	var s stateData
	if err := json.Unmarshal(data, &s); err != nil {
		log.Printf("bdtieba: posts state parse error: %v", err)
		return
	}
	if s != nil {
		pstate = s
	}
}

func savePstate() {
	data, err := json.MarshalIndent(pstate, "", "  ")
	if err != nil {
		log.Printf("bdtieba: posts state marshal error: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(pstatePath()), 0700); err != nil {
		log.Printf("bdtieba: posts state mkdir error: %v", err)
		return
	}
	if err := os.WriteFile(pstatePath(), data, 0600); err != nil {
		log.Printf("bdtieba: posts state write error: %v", err)
	}
}

func ensurePstate() {
	pstateMu.Lock()
	defer pstateMu.Unlock()
	if pstate != nil {
		return
	}
	pstate = stateData{}
	loadPstate()
	prunePstate(time.Now())
	if len(pstate) > 0 {
		log.Printf("bdtieba: loaded %d pids in posts dedupe window", len(pstate))
	}
}

func prunePstate(now time.Time) {
	cutoff := now.Add(-postDedupeExpiry).Unix()
	for pid, seen := range pstate {
		if seen < cutoff {
			delete(pstate, pid)
		}
	}
}

// ProcessNewPosts fetches one page of floors of thread tid, keeps a persistent
// dedupe set of already-seen post ids, logs every new floor and returns it.
// It does not publish. Seen post ids are persisted to
// ~/.config/fedlet/bdtieba-posts-state.json with a 72h expiry window.
func ProcessNewPosts(tid int64, pn, rn int) ([]Post, error) {
	ensurePstate()

	data, err := FetchPosts(tid, pn, rn)
	if err != nil {
		return nil, err
	}

	pstateMu.Lock()
	checked, dup := 0, 0
	var out []Post
	for i := range data.PostList {
		p := &data.PostList[i]
		checked++
		if _, seen := pstate[p.ID]; seen {
			dup++
			continue
		}
		out = append(out, *p)
		pstate[p.ID] = time.Now().Unix()
		log.Printf("bdtieba: [%s] post floor=%d pid=%d %s %s",
			forumName(data.Forum, ""), p.Floor, p.ID, authorNick(p.Author), truncate(postText(p), 120))
	}
	prunePstate(time.Now())
	if dup > 0 {
		log.Printf("bdtieba: [%s] post dedupe skipped %d/%d (new=%d)",
			forumName(data.Forum, ""), dup, checked, len(out))
	}
	if len(out) > 0 {
		savePstate()
	}
	pstateMu.Unlock()
	return out, nil
}

func postText(p *Post) string {
	var sb strings.Builder
	for _, f := range p.Content {
		if f.Text != "" {
			sb.WriteString(f.Text)
		}
	}
	return strings.TrimSpace(sb.String())
}

// FetchLatestPosts returns the newest n floors of thread tid, ordered by time
// descending (newest first). n<=0 defaults to 5; each request pulls up to 30
// floors (endpoint cap), so walks older pages only when more than one page is
// needed. Pure fetch: no dedupe, no logging, no publishing.
func FetchLatestPosts(tid int64, n int) ([]Post, error) {
	if tid <= 0 {
		return nil, fmt.Errorf("bdtieba: invalid tid %d", tid)
	}
	if n <= 0 {
		n = 5
	}

	var collected []Post
	pn := postJumpPage
	for {
		data, err := FetchPosts(tid, pn, postPageSize)
		if err != nil {
			return nil, err
		}
		collected = append(collected, data.PostList...)
		if len(collected) >= n {
			break
		}
		if data.Page.HasMore != 1 {
			break
		}
		if pn == postJumpPage {
			pn = data.Page.TotalPage - 1
		} else {
			pn--
		}
		if pn < 1 {
			break
		}
	}
	if len(collected) == 0 {
		return nil, nil
	}
	sort.Slice(collected, func(i, j int) bool { return collected[i].Time > collected[j].Time })
	if len(collected) > n {
		collected = collected[:n]
	}
	return collected, nil
}

// ProcessLatestPosts fetches the newest n floors of thread tid (see
// FetchLatestPosts), keeps the persistent post-id dedupe set, logs every new
// floor and returns it. It does not publish.
func ProcessLatestPosts(tid int64, n int) ([]Post, error) {
	ensurePstate()

	posts, err := FetchLatestPosts(tid, n)
	if err != nil {
		return nil, err
	}
	if len(posts) == 0 {
		return nil, nil
	}

	pstateMu.Lock()
	checked, dup := 0, 0
	var out []Post
	for i := range posts {
		p := &posts[i]
		checked++
		if _, seen := pstate[p.ID]; seen {
			dup++
			continue
		}
		out = append(out, *p)
		pstate[p.ID] = time.Now().Unix()
		log.Printf("bdtieba: post tid=%d floor=%d pid=%d %s %s",
			tid, p.Floor, p.ID, authorNick(p.Author), truncate(postText(p), 120))
	}
	prunePstate(time.Now())
	if dup > 0 {
		log.Printf("bdtieba: post tid=%d dedupe skipped %d/%d (new=%d)", tid, dup, checked, len(out))
	}
	if len(out) > 0 {
		savePstate()
	}
	pstateMu.Unlock()
	return out, nil
}
