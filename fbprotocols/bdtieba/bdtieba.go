package bdtieba

import (
	"encoding/json"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	dedupeExpiry    = 72 * time.Hour
	minReqInterval  = 60 * time.Second
	defaultInterval = 600 * time.Second
)

var defaultKws = []string{"linux", "android", "个人电脑", "游戏吧", "gpt", "ai人工智能", "2ch", "方便面", "吊图", "孙笑川"}

var (
	pubfn_      func(any) error
	muClient    sync.Mutex
	curKws      []string
	curInterval time.Duration
)

func SetPublishInfo(pubfn func(any) error) {
	pubfn_ = pubfn
}

func publish(v any) error {
	if pubfn_ == nil {
		return nil
	}
	return pubfn_(v)
}

// threadSeen 记录一个 tid 的排重状态。ReplyNum 为该帖已处理的回复数;
// ReplyNum<0 表示从旧版本状态文件迁移而来,回复基线未知(仅去重、不补拉)。
// Seen 为首见时间戳(72h 过期)。
type threadSeen struct {
	ReplyNum int64 `json:"reply_num"`
	Seen     int64 `json:"seen"`
}

// threadState 帖子排重集合:key=tid,保证每个帖子 metadata 只 publish 一次。
type threadState map[int64]*threadSeen

func Start(kws []string, interval time.Duration) {
	if len(kws) == 0 {
		kws = defaultKws
	}
	if interval <= 0 {
		interval = defaultInterval
	}
	muClient.Lock()
	curKws = append([]string(nil), kws...)
	curInterval = interval
	muClient.Unlock()

	go pollLoop()
}

func pollLoop() {
	statusRunning.Store(true)
	statusConnectedSince.Store(time.Now())
	defer statusRunning.Store(false)

	muClient.Lock()
	kws := append([]string(nil), curKws...)
	interval := curInterval
	muClient.Unlock()
	log.Printf("bdtieba: polling %d forums every %s", len(kws), interval)

	statePath := stateFilePath()
	state := loadState(statePath)
	now := time.Now()
	pruneState(state, now)
	if len(state) > 0 {
		log.Printf("bdtieba: loaded %d tids in dedupe window", len(state))
	}

	for {
		// 每轮随机打乱吧表顺序,降低 WAF 对固定扫描顺序的可预测性。
		rand.Shuffle(len(kws), func(i, j int) { kws[i], kws[j] = kws[j], kws[i] })
		for _, kw := range kws {
			data, err := FetchFrs(kw, 1, 30)
			if err != nil {
				log.Printf("bdtieba: poll %q error: %v", kw, err)
				pushError(err)
				time.Sleep(minReqInterval)
				continue
			}

			processThreads(kw, data, state, time.Now())

			now := time.Now()
			pruneState(state, now)
			saveState(statePath, state)
			time.Sleep(minReqInterval)
		}
		time.Sleep(interval)
	}
}

// processThreads 按 tid 去重,保证每个帖子 metadata 只 publish 一次:
// 首见:发布 metadata + 执行新回复流程;
// 已见且 reply_num 增加:不重发 metadata,仅执行回复增量流程(pid 去重只发新楼);
// 已见且 reply_num 未变(或迁移基线未知):跳过。IsTop 置顶帖保持跳过。
func processThreads(kw string, d *FrsData, state threadState, now time.Time) (newN, dupN int) {
	checked, dup, n := 0, 0, 0
	// 请求与列表排序保持不变(置顶优先+last_time_int 降序),仅反向遍历:
	// 发布/日志顺序 = 最久未回复在前,与楼层回复的返回顺序修复一致。
	for i := len(d.ThreadList) - 1; i >= 0; i-- {
		t := &d.ThreadList[i]
		if t.IsTop == 1 {
			continue
		}
		checked++
		prev, seen := state[t.Tid]
		if seen {
			if prev == nil {
				// 防御:正常由 loadState 过滤,此处视为已见,仅校准基线。
				state[t.Tid] = &threadSeen{ReplyNum: t.ReplyNum, Seen: now.Unix()}
				dup++
				continue
			}
			if prev.ReplyNum < 0 {
				// 旧格式迁移:基线未知,首轮校准为当前值(不发布、不补拉),
				// 保留 Seen 时间戳;下次 reply_num 增加即可触发回复增量。
				prev.ReplyNum = t.ReplyNum
				dup++
				continue
			}
			if t.ReplyNum <= prev.ReplyNum {
				dup++
				continue
			}
		}
		n++
		if !seen {
			log.Printf("bdtieba: [%s] %s (tid=%d reply=%d) %s",
				forumName(d.Forum, kw), truncate(t.Title, 80), t.Tid, t.ReplyNum, authorNick(t.Author))
			if err := publish(publishPayload(d.Forum, t)); err != nil {
				log.Printf("bdtieba: publish %q tid=%d error: %v", kw, t.Tid, err)
			}
		}
		processThreadReplies(kw, d.Forum, t)
		state[t.Tid] = &threadSeen{ReplyNum: t.ReplyNum, Seen: now.Unix()}
	}
	if dup > 0 {
		log.Printf("bdtieba: [%s] dedupe skipped %d/%d (new=%d)", forumName(d.Forum, kw), dup, checked, n)
	}
	return n, dup
}

const newPostReplies = 5

// processThreadReplies 立即为新 key 的帖子拉取最新 newPostReplies 楼,
// 经 pid 去重(ProcessLatestPosts,内部日志+持久化)后逐个 publish。
func processThreadReplies(kw string, f Forum, t *Thread) {
	posts, err := ProcessLatestPosts(t.Tid, newPostReplies)
	if err != nil {
		log.Printf("bdtieba: posts %q tid=%d error: %v", kw, t.Tid, err)
		return
	}
	for i := range posts {
		if err := publish(publishPostPayload(f, t, &posts[i])); err != nil {
			log.Printf("bdtieba: publish post %q tid=%d pid=%d error: %v", kw, t.Tid, posts[i].ID, err)
		}
	}
}

func publishPostPayload(f Forum, t *Thread, p *Post) map[string]any {
	return map[string]any{
		"forum":  f,
		"thread": t,
		"post":   p,
	}
}

// publishPayload builds the raw (non-unified) payload sent to downstream.
func publishPayload(f Forum, t *Thread) map[string]any {
	return map[string]any{
		"forum":  f,
		"thread": t,
	}
}

func forumName(f Forum, kw string) string {
	if f.Name != "" {
		return f.Name
	}
	return kw
}

func authorNick(a *Author) string {
	if a == nil {
		return ""
	}
	if a.ShowNickname != "" {
		return a.ShowNickname
	}
	return a.NameShow
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

func pruneState(state threadState, now time.Time) {
	cutoff := now.Add(-dedupeExpiry).Unix()
	for tid, e := range state {
		if e != nil && e.Seen < cutoff {
			delete(state, tid)
		}
	}
}

func stateFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "fedlet", "bdtieba-state.json")
}

func loadState(path string) threadState {
	data, err := os.ReadFile(path)
	if err != nil {
		return threadState{}
	}

	// 新格式:{"<tid>": {"reply_num": N, "seen": T}}。key 为纯数字,
	// 而旧格式 key 含点,json 反序列化到 map[int64] 会失败,自动走旧格式分支。
	var s threadState
	if err := json.Unmarshal(data, &s); err == nil && s != nil {
		for tid, e := range s {
			if e == nil {
				delete(s, tid)
			}
		}
		return s
	}

	// 旧格式:{"tid.reply_num": timestamp} → 迁移为 tid{ReplyNum:-1, Seen:timestamp}。
	// ReplyNum=-1 表示回复基线未知:仅保留"已发布 metadata"去重,不触发补拉。
	var old map[string]int64
	if err := json.Unmarshal(data, &old); err != nil && len(data) > 0 {
		log.Printf("bdtieba: load state parse error: %v", err)
		return threadState{}
	}
	s2 := make(threadState, len(old))
	for k, v := range old {
		if dot := strings.IndexByte(k, '.'); dot > 0 {
			k = k[:dot]
		}
		tid, err := strconv.ParseInt(k, 10, 64)
		if err != nil || tid <= 0 {
			continue
		}
		if _, ok := s2[tid]; !ok {
			s2[tid] = &threadSeen{ReplyNum: -1, Seen: v}
		}
	}
	if len(s2) > 0 {
		log.Printf("bdtieba: migrated %d tids from old state format", len(s2))
	}
	return s2
}

func saveState(path string, s threadState) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		log.Printf("bdtieba: save state marshal error: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		log.Printf("bdtieba: save state mkdir error: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("bdtieba: save state write error: %v", err)
	}
}

// protocol status
var (
	statusRunning        atomic.Bool
	statusConnectedSince atomic.Value
	statusLastErrsMu     sync.Mutex
	statusLastErrs       [3]error
)

func pushError(err error) {
	statusLastErrsMu.Lock()
	statusLastErrs[2] = statusLastErrs[1]
	statusLastErrs[1] = statusLastErrs[0]
	statusLastErrs[0] = err
	statusLastErrsMu.Unlock()
}

func IsRunning() bool { return statusRunning.Load() }

func ConnectedSince() time.Time {
	v := statusConnectedSince.Load()
	if v == nil {
		return time.Time{}
	}
	return v.(time.Time)
}

func LastErrs() []error {
	statusLastErrsMu.Lock()
	defer statusLastErrsMu.Unlock()
	var out []error
	for _, e := range statusLastErrs {
		if e != nil {
			out = append(out, e)
		}
	}
	return out
}
