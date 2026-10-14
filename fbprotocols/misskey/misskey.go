package misskey

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

var (
	pubfn_      func(any) error
	muClient    sync.Mutex
	curHost     string
	curToken    string
	curTL       string
	accountId   string
	accountName string
	cfgProvider func() (host, token, tl string)
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

// currentCfg 返回最新配置:优先用 Start 注入的 getter(可热改),否则回退
// 到 Start 时缓存的 curHost/curToken/curTL(测试与冷启动路径)。
func currentCfg() (string, string, string) {
	muClient.Lock()
	defer muClient.Unlock()
	if cfgProvider != nil {
		return cfgProvider()
	}
	return curHost, curToken, curTL
}

func Start(getHost, getToken, getTimeline func() string) {
	host, token, timeline := getHost(), getToken(), getTimeline()
	if host == "" || token == "" {
		log.Println("misskey: host and token required")
		return
	}
	if timeline == "" {
		timeline = "home"
	}
	muClient.Lock()
	curHost = host
	curToken = token
	curTL = timeline
	cfgProvider = func() (string, string, string) {
		tl := getTimeline()
		if tl == "" {
			tl = "home"
		}
		return getHost(), getToken(), tl
	}
	muClient.Unlock()

	info, err := VerifyToken(host, token)
	if err != nil {
		log.Printf("misskey: token verification failed: %v", err)
		return
	}
	log.Printf("misskey: verified as @%s (%s)", info.Username, info.Name)
	accountHost := strings.TrimRight(host, "/")
	accountHost = strings.TrimPrefix(accountHost, "https://")
	accountHost = strings.TrimPrefix(accountHost, "http://")
	accountId = "@" + info.Username + "@" + accountHost

	meta, err := FetchMeta(host)
	if err != nil {
		log.Printf("misskey: meta error: %v", err)
	} else {
		log.Printf("misskey: server %s (v%s)", meta.Name, meta.Version)
	}

	go pollLoop()
}

func pollLoop() {
	statusRunning.Store(true)
	statusConnectedSince.Store(time.Now())
	defer statusRunning.Store(false)
	_, _, tl0 := currentCfg()
	log.Printf("misskey: polling %s timeline every 30s", tl0)

	statePath := stateFilePath()
	state := loadState(statePath)

	firstRun := state.SinceID == ""

	for {
		host, token, tl := currentCfg()

		notes, raws, err := FetchTimeline(host, token, tl, state.SinceID)
		if err != nil {
			log.Printf("misskey: poll error: %v", err)
			pushError(err)
			time.Sleep(30 * time.Second)
			continue
		}

		if firstRun {
			if len(notes) > 0 {
				state.SinceID = notes[0].ID
				saveState(statePath, state)
				log.Printf("misskey: initial sync, caught up to %s (%d notes seen)", state.SinceID, len(notes))
			}
			firstRun = false
			time.Sleep(30 * time.Second)
			continue
		}

		for i := len(notes) - 1; i >= 0; i-- {
			n := notes[i]
			if n.Text == "" {
				continue
			}
			log.Printf("misskey: @%s: %s", n.User.Username, truncate(n.Text, 80))
			ev, err := toRawEvent(n, raws[i])
			if err != nil {
				log.Printf("misskey: raw event: %v", err)
				continue
			}
			if err := publish(ev); err != nil {
				log.Printf("misskey: publish raw error: %v", err)
			}
			um, ok := n.toUnified(raws[i])
			if ok {
				if err := publish(um); err != nil {
					log.Printf("misskey: publish um error: %v", err)
				}
			}
		}

		if len(notes) > 0 {
			state.SinceID = notes[0].ID
			saveState(statePath, state)
		}

		time.Sleep(30 * time.Second)
	}
}

// validVisibilities 为 Misskey notes/create 的可见性白名单。
var validVisibilities = map[string]bool{
	"public":    true,
	"home":      true,
	"followers": true,
	"specified": true,
}

// resolveVisibility 确定 note 可见性。
// 优先级:extra.Visibility 显式字段 → 兼容旧约定的 to 关键词 → 默认 home;
// 非白名单显式值直接报错,不再静默降级。
func resolveVisibility(to string, extra *fbshared.SendExtra) (string, error) {
	v := "home"
	if extra != nil && extra.Visibility != "" {
		v = extra.Visibility
	} else if validVisibilities[to] {
		v = to
	}
	if !validVisibilities[v] {
		return "", fmt.Errorf("misskey: unsupported visibility %q (want public/home/followers/specified)", v)
	}
	return v, nil
}

func Send(to, msg, msgType string, filedata []byte, fileinfo *fbshared.MediaDataInfo, extra *fbshared.SendExtra) (fbshared.SendResult, error) {
	if msg == "" {
		return fbshared.SendResult{}, fmt.Errorf("misskey: empty message")
	}
	if len(filedata) > 0 && fileinfo == nil {
		return fbshared.SendResult{}, fmt.Errorf("misskey: filedata present but fileinfo is nil")
	}
	visibility, err := resolveVisibility(to, extra)
	if err != nil {
		return fbshared.SendResult{}, err
	}
	var visibleUserIds []string
	if visibility == "specified" {
		if extra == nil || len(extra.RelatesTo) == 0 {
			return fbshared.SendResult{}, fmt.Errorf("misskey: specified visibility requires relates_to recipients")
		}
		visibleUserIds = extra.RelatesTo
	}
	host, token, _ := currentCfg()
	if host == "" || token == "" {
		return fbshared.SendResult{}, fmt.Errorf("misskey: not configured")
	}
	var fileIds []string
	if len(filedata) > 0 {
		filename := fileinfo.Filename
		if filename == "" {
			filename = "untitled"
		}
		fileID, err := uploadDriveFile(host, token, filedata, filename)
		if err != nil {
			return fbshared.SendResult{}, fmt.Errorf("misskey: upload: %w", err)
		}
		fileIds = []string{fileID}
	}
	log.Printf("misskey: sending [%s]: %s", visibility, truncate(msg, 80))
	noteID, err := SendNote(host, token, msg, visibility, visibleUserIds, fileIds)
	if err == nil {
		log.Printf("misskey: sent note_id=%s", noteID)
	}
	return fbshared.SendResult{MsgID: noteID}, err
}

// Redact 删除本账号的 note。to 为目标身份标识,Misskey 删除仅需 note_id,故不使用;
// reason 也被 notes/delete 忽略,仅记日志备查。
func Redact(to, noteID, reason string) (fbshared.SendResult, error) {
	if noteID == "" {
		return fbshared.SendResult{}, fmt.Errorf("misskey: empty note id")
	}
	host, token, _ := currentCfg()
	if host == "" || token == "" {
		return fbshared.SendResult{}, fmt.Errorf("misskey: not configured")
	}
	log.Printf("misskey: deleting note_id=%s reason=%q", noteID, reason)
	if err := DeleteNote(host, token, noteID); err != nil {
		return fbshared.SendResult{}, err
	}
	log.Printf("misskey: deleted note_id=%s", noteID)
	return fbshared.SendResult{MsgID: noteID}, nil
}

func stateFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "fedlet", "misskey-state.json")
}

func loadState(path string) *stateData {
	data, err := os.ReadFile(path)
	if err != nil {
		return &stateData{}
	}
	var s stateData
	json.Unmarshal(data, &s)
	return &s
}

func saveState(path string, s *stateData) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		log.Printf("misskey: save state marshal error: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		log.Printf("misskey: save state mkdir error: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("misskey: save state write error: %v", err)
	}
}

// protocol status
var (
	statusRunning        atomic.Bool
	statusConnectedSince atomic.Value // time.Time
	statusReconnTimes    atomic.Int64
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
func ReconnTimes() int64 { return statusReconnTimes.Load() }
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

// toRawEvent 由 API 原始字节构建原结构事件并注入 account_* 补丁字段。
func toRawEvent(n Note, raw []byte) (map[string]any, error) {
	var ev map[string]any
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, err
	}
	ev["account_id"] = n.AccountID
	ev["account_name"] = n.AccountName
	return ev, nil
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

func (n *Note) toUnified(raw []byte) (fbshared.UnifiedMessage, bool) {
	um := fbshared.UnifiedMessage{
		Text:        n.Text,
		MsgFormat:   fbshared.FmtText,
		Protocol:    fbshared.ProtoMisskey,
		Username:    n.User.Username,
		UserID:      n.UserID,
		Usernick:    n.User.Name,
		MsgID:       n.ID,
		MsgType:     fbshared.MsgTypeCreate,
		AccountID:   n.AccountID,
		AccountName: n.AccountName,
	}

	if t, err := time.Parse(time.RFC3339, n.CreatedAt); err == nil {
		um.Timestamp = t.UnixNano()
	}
	um.Raw = raw
	return um, true
}
