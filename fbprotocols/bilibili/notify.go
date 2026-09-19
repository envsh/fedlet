package bilibili

// Notifications: unread counts + the reply / @ / like / sys event lists. Needs
// SESSDATA. The unread aggregate is published every round (kind=notify_unread);
// when it changes, the event lists are pulled and new events published
// individually (kind=notify_event). Feed comes from the /x/msgfeed/* family
// (the old /x/msg/* endpoints are retired with a global gateway 404).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

// The old /x/msg/push-info/unread and /x/msg/reply|at|like endpoints are
// retired: bilibili's gateway serves a global 404 HTML page for the whole
// /x/msg/ namespace. The current message-center family is /x/msgfeed/* on
// apiHost (unread/reply/at/like) plus the sys feed on the message subdomain.
const (
	unreadURL = apiHost + "/x/msgfeed/unread"
	replyURL  = apiHost + "/x/msgfeed/reply"
	atURL     = apiHost + "/x/msgfeed/at"
	likeURL   = apiHost + "/x/msgfeed/like"
)

// msgHost is the message-center subdomain; /x/msgfeed/* carries no system
// notifications, so those come from here.
const (
	msgHost   = "https://message.bilibili.com"
	sysMsgURL = msgHost + "/x/sys-msg/query_user_notify"
)

// unmarshalUseNumber decodes JSON preserving exact numeric digits (json.Number)
// inside map[string]any. Without it, numbers decode as float64 and the large
// msgfeed ids (subject_id ~1e18) silently lose precision beyond 2^53.
func unmarshalUseNumber(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
}

// unreadCounts is the shape of /x/msgfeed/unread. SysMsg is tracked so the
// aggregate reflects the full notify center; Total is computed in code (the
// msgfeed payload has no total field).
type unreadCounts struct {
	Reply  int64 `json:"reply"`
	At     int64 `json:"at"`
	Like   int64 `json:"like"`
	SysMsg int64 `json:"sys_msg"`
	// counts for other biz types (coin/danmu/favorite/up/chat) are ignored;
	// only reply/at/like/sys events are fanned out to detail lists.
	Total int64 `json:"-"`
}

// unmarshalUnread decodes the /x/msgfeed/unread payload and computes the total
// across the tracked biz types.
func unmarshalUnread(data json.RawMessage) (unreadCounts, error) {
	var u unreadCounts
	if err := json.Unmarshal(data, &u); err != nil {
		return u, err
	}
	u.Total = u.Reply + u.At + u.Like + u.SysMsg
	return u, nil
}

// notifyEvent is a flattened detail-list event shared by reply/at/like.
type notifyEvent struct {
	ID             int64
	Type           string
	Ctime          int64
	Uname          string
	Mid            int64
	Author         map[string]any // 原始 user / publisher 对象(含 avatar/face、fans、follow、mid_link)
	Avatar         string
	Message        string
	DynamicURL     string
	Subject        string
	Image          string
	Desc           string
	ItemType       string
	Business       string
	SubjectID      int64
	SourceID       int64
	TargetID       int64
	RootReply      string
	TargetReply    string
	AtDetails      []any
	TopicDetails   []any
	Counts         int64 // like 点赞人数(counts)
	ItemCtime      int64 // like 内容创建时间(item.ctime)
	NotifyType     int64 // sys type
	CardType       int64
	CardBrief      string
	CardMsgBrief   string
	CardStoryTitle string
	Source         map[string]any
}

// fetchUnread pulls the aggregate unread counter.
func fetchUnread() (*unreadCounts, error) {
	if jar.get("SESSDATA") == "" {
		return nil, ErrNotLoggedIn
	}
	body, status, err := doBili(http.MethodGet, unreadURL, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("bilibili: unread http %d", status)
	}
	var env envResp
	if err := json.Unmarshal(body, &env); err != nil {
		if looksHTML(body) {
			return nil, fmt.Errorf("%w %s", errUnverifiable, unreadURL)
		}
		return nil, fmt.Errorf("bilibili: unread parse: %w", err)
	}
	if env.Code != 0 {
		return nil, classifyAPIError(unreadURL, env.Code, env.Message)
	}
	data, err := unmarshalUnread(env.Data)
	if err != nil {
		return nil, fmt.Errorf("bilibili: unread data parse: %w", err)
	}
	return &data, nil
}

// fetchReplyEvents pulls the newest content reply, @, like and sys events.
func fetchReplyEvents() ([]notifyEvent, error) {
	return fetchNotifyList(replyURL, "reply")
}

func fetchAtEvents() ([]notifyEvent, error) {
	return fetchNotifyList(atURL, "at")
}

func fetchLikeEvents() ([]notifyEvent, error) {
	return fetchLikeList()
}

func fetchSysEvents() ([]notifyEvent, error) {
	return fetchSysNotify()
}

// fetchNotifyList fetches one /x/msgfeed list. The reply/at entries share one
// shape: {id, user{mid,nickname}, item{...}, reply_time}.
func fetchNotifyList(u, kind string) ([]notifyEvent, error) {
	if jar.get("SESSDATA") == "" {
		return nil, ErrNotLoggedIn
	}
	body, status, err := doBili(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("bilibili: notify %s http %d", kind, status)
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := unmarshalUseNumber(body, &env); err != nil {
		if looksHTML(body) {
			return nil, fmt.Errorf("%w %s", errUnverifiable, u)
		}
		return nil, fmt.Errorf("bilibili: notify %s parse: %w", kind, err)
	}
	if env.Code != 0 {
		return nil, classifyAPIError(u, env.Code, "")
	}
	return parseMsgfeedItems(env.Data.Items, kind), nil
}

// parseMsgfeedItems flattens reply/at notification entries into events.
func parseMsgfeedItems(items []map[string]any, kind string) []notifyEvent {
	events := make([]notifyEvent, 0, len(items))
	for _, m := range items {
		ev := notifyEvent{Type: kind}
		ev.ID, _ = numAnyAsInt(m["id"])
		ev.Ctime, _ = numAnyAsInt(m["reply_time"])
		if ua, ok := m["user"].(map[string]any); ok {
			ev.Uname, _ = ua["nickname"].(string)
			ev.Mid, _ = numAnyAsInt(ua["mid"])
			ev.Author = ua
			ev.Avatar, _ = ua["avatar"].(string)
		}
		if it, ok := m["item"].(map[string]any); ok {
			for _, k := range []string{"message", "root_reply_content", "source_content", "note", "title"} {
				if s, ok := it[k].(string); ok && s != "" {
					ev.Message = s
					break
				}
			}
			ev.Subject, _ = it["title"].(string)
			if ev.Subject == "" {
				ev.Subject, _ = it["business"].(string)
			}
			ev.DynamicURL, _ = it["uri"].(string)
			ev.Image, _ = it["image"].(string)
			ev.Desc, _ = it["desc"].(string)
			ev.ItemType, _ = it["type"].(string)
			ev.Business, _ = it["business"].(string)
			ev.SubjectID, _ = numAnyAsInt(it["subject_id"])
			ev.SourceID, _ = numAnyAsInt(it["source_id"])
			ev.TargetID, _ = numAnyAsInt(it["target_id"])
			ev.RootReply, _ = it["root_reply_content"].(string)
			ev.TargetReply, _ = it["target_reply_content"].(string)
			ev.AtDetails, _ = it["at_details"].([]any)
			ev.TopicDetails, _ = it["topic_details"].([]any)
		}
		ev.Counts, _ = numAnyAsInt(m["counts"])
		events = append(events, ev)
	}
	return events
}

// fetchLikeList fetches /x/msgfeed/like. Likes are grouped per target with the
// recent likers in users[]; the all-time total.items carry the full set
// (latest.items only those since last_view_at). Dedupe in the round drops
// repeats either way.
func fetchLikeList() ([]notifyEvent, error) {
	if jar.get("SESSDATA") == "" {
		return nil, ErrNotLoggedIn
	}
	body, status, err := doBili(http.MethodGet, likeURL, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("bilibili: notify like http %d", status)
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			Latest struct {
				Items []map[string]any `json:"items"`
			} `json:"latest"`
			Total struct {
				Items []map[string]any `json:"items"`
			} `json:"total"`
		} `json:"data"`
	}
	if err := unmarshalUseNumber(body, &env); err != nil {
		if looksHTML(body) {
			return nil, fmt.Errorf("%w %s", errUnverifiable, likeURL)
		}
		return nil, fmt.Errorf("bilibili: notify like parse: %w", err)
	}
	if env.Code != 0 {
		return nil, classifyAPIError(likeURL, env.Code, "")
	}
	items := env.Data.Total.Items
	if len(items) == 0 {
		items = env.Data.Latest.Items
	}
	return parseLikeItems(items), nil
}

// parseLikeItems flattens /x/msgfeed/like entries into events.
func parseLikeItems(items []map[string]any) []notifyEvent {
	events := make([]notifyEvent, 0, len(items))
	for _, m := range items {
		ev := notifyEvent{Type: "like"}
		ev.ID, _ = numAnyAsInt(m["id"])
		ev.Ctime, _ = numAnyAsInt(m["like_time"])
		if users, ok := m["users"].([]any); ok && len(users) > 0 {
			if u0, ok := users[0].(map[string]any); ok {
				ev.Uname, _ = u0["nickname"].(string)
				ev.Mid, _ = numAnyAsInt(u0["mid"])
				ev.Author = u0
				ev.Avatar, _ = u0["avatar"].(string)
			}
		}
		ev.Message = "赞了你的内容"
		if it, ok := m["item"].(map[string]any); ok {
			ev.Subject, _ = it["title"].(string)
			if ev.Subject == "" {
				ev.Subject, _ = it["business"].(string)
			}
			ev.DynamicURL, _ = it["uri"].(string)
			ev.Image, _ = it["image"].(string)
			ev.Desc, _ = it["desc"].(string)
			ev.ItemType, _ = it["type"].(string)
			ev.Business, _ = it["business"].(string)
			ev.SubjectID, _ = numAnyAsInt(it["item_id"])
			ev.ItemCtime, _ = numAnyAsInt(it["ctime"])
		}
		ev.Counts, _ = numAnyAsInt(m["counts"])
		events = append(events, ev)
	}
	return events
}

// fetchSysNotify pulls the system-notification feed from the message-center
// subdomain. The envelope and item shapes differ from the /x/msgfeed family
// and the host expects a message.bilibili.com referer, so this request is made
// with its own headers rather than the shared doBili defaults.
func fetchSysNotify() ([]notifyEvent, error) {
	if jar.get("SESSDATA") == "" {
		return nil, ErrNotLoggedIn
	}
	req, err := http.NewRequest(http.MethodGet, sysMsgURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", biliUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Referer", msgHost+"/")
	req.Header.Set("Cookie", jar.header())
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		if looksHTML(body) {
			return nil, fmt.Errorf("%w http %d %s", errUnverifiable, resp.StatusCode, sysMsgURL)
		}
		return nil, fmt.Errorf("bilibili: notify sys http %d", resp.StatusCode)
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			List []map[string]any `json:"system_notify_list"`
		} `json:"data"`
	}
	if err := unmarshalUseNumber(body, &env); err != nil {
		if looksHTML(body) {
			return nil, fmt.Errorf("%w %s", errUnverifiable, sysMsgURL)
		}
		return nil, fmt.Errorf("bilibili: notify sys parse: %w", err)
	}
	if env.Code != 0 {
		return nil, classifyAPIError(sysMsgURL, env.Code, "")
	}
	return parseSysItems(env.Data.List), nil
}

// parseSysItems flattens system_notify_list entries into events.
func parseSysItems(list []map[string]any) []notifyEvent {
	events := make([]notifyEvent, 0, len(list))
	for _, m := range list {
		ev := notifyEvent{Type: "sys"}
		ev.ID, _ = numAnyAsInt(m["id"])
		ev.Ctime = parseSysTime(m["time_at"])
		ev.Subject, _ = m["title"].(string)
		ev.Message, _ = m["content"].(string)
		ev.DynamicURL, _ = m["card_link"].(string)
		ev.NotifyType, _ = numAnyAsInt(m["type"])
		ev.CardType, _ = numAnyAsInt(m["card_type"])
		ev.CardBrief, _ = m["card_brief"].(string)
		ev.CardMsgBrief, _ = m["card_msg_brief"].(string)
		ev.CardStoryTitle, _ = m["card_story_title"].(string)
		ev.Image, _ = m["card_cover"].(string)
		ev.ItemType = strconv.FormatInt(ev.NotifyType, 10)
		if p, ok := m["publisher"].(map[string]any); ok {
			ev.Uname, _ = p["name"].(string)
			ev.Mid, _ = numAnyAsInt(p["mid"])
			ev.Author = p
			ev.Avatar, _ = p["face"].(string)
		}
		if src, ok := m["source"].(map[string]any); ok {
			ev.Source = src
			if ev.Image == "" {
				ev.Image, _ = src["logo"].(string)
			}
		}
		events = append(events, ev)
	}
	return events
}

// parseSysTime converts the message-center "2006-01-02 15:04:05" (UTC+8)
// timestamp to a unix value; anything else yields 0.
func parseSysTime(v any) int64 {
	s, ok := v.(string)
	if !ok || s == "" {
		return 0
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.FixedZone("CST", 8*3600))
	if err != nil {
		return 0
	}
	return t.Unix()
}

// notifyLastUnread remembers the last-published aggregate so rounds report
// changes without spamming.
var notifyLastUnread *unreadCounts

// notifyRouteBlockedUntil pauses the notify round while the message-center
// endpoints answer with gateway HTML (errUnverifiable, e.g. a WAF block), so
// it is not hammered and re-logged every interval; the round resumes after the
// window and keeps quiet when the endpoints come back.
var notifyRouteBlockedUntil time.Time

// notifyBlockProbeInterval is how long a message-center HTML block stays
// silent before probing again. bilibili anti-bot blocks typically lift within
// minutes to an hour.
var notifyBlockProbeInterval = 30 * time.Minute

// isWAFGate reports the hard gateway HTML blocks (200/404/522 anti-bot pages)
// that make the whole round pointless and noisy.
func isWAFGate(err error) bool {
	return errors.Is(err, errUnverifiable)
}

// notifyRound publishes the aggregate + new detail events. Returns the count
// of detail events published.
func notifyRound(state *biliState) int {
	if time.Now().Before(notifyRouteBlockedUntil) {
		return 0
	}
	un, err := fetchUnread()
	if err != nil {
		if isWAFGate(err) {
			notifyRouteBlockedUntil = time.Now().Add(notifyBlockProbeInterval)
			log.Printf("bilibili: notify blocked (WAF HTML), pausing ~%s: %v", notifyBlockProbeInterval, err)
			pushError(err)
			return 0
		}
		log.Printf("bilibili: notify unread error: %v", err)
		pushError(err)
		return 0
	}
	now := time.Now()
	if notifyLastUnread == nil || !sameUnread(notifyLastUnread, un) {
		payload := map[string]any{
			"kind":         "notify_unread",
			"reply":        un.Reply,
			"at":           un.At,
			"like":         un.Like,
			"sys_msg":      un.SysMsg,
			"total":        un.Total,
			"published_at": now.Unix(),
		}
		if err := publish(payload); err != nil {
			log.Printf("bilibili: publish unread error: %v", err)
		}
	}
	notifyLastUnread = un

	// First sync: seed the dedupe keys so existing history is not re-published.
	if len(state.Notifications) == 0 {
		for _, fn := range []func() ([]notifyEvent, error){fetchReplyEvents, fetchAtEvents, fetchLikeEvents, fetchSysEvents} {
			evs, err := fn()
			if err != nil {
				if !isWAFGate(err) {
					log.Printf("bilibili: notify seed error: %v", err)
				}
				continue
			}
			for _, e := range evs {
				state.Notifications[notifyKey(&e)] = now.Unix()
			}
		}
		log.Printf("bilibili: notify first sync, seeded %d existing events", len(state.Notifications))
		return 0
	}

	published := 0
	handle := func(kind string, evs []notifyEvent) {
		for i := range evs {
			it := &evs[i]
			key := notifyKey(it)
			if _, seen := state.Notifications[key]; seen {
				continue
			}
			state.Notifications[key] = now.Unix()
			log.Printf("bilibili: notify %s id=%d by %s %s", kind, it.ID, it.Uname, truncate(it.Message, 40))
			payload := map[string]any{
				"kind":                 "notify_event",
				"event_type":           kind,
				"id":                   it.ID,
				"actor":                it.Uname,
				"actor_mid":            it.Mid,
				"author":               it.Author,
				"avatar":               it.Avatar,
				"message":              it.Message,
				"subject":              it.Subject,
				"url":                  it.DynamicURL,
				"image":                it.Image,
				"desc":                 it.Desc,
				"item_type":            it.ItemType,
				"business":             it.Business,
				"subject_id":           it.SubjectID,
				"source_id":            it.SourceID,
				"target_id":            it.TargetID,
				"root_reply_content":   it.RootReply,
				"target_reply_content": it.TargetReply,
				"at_details":           it.AtDetails,
				"topic_details":        it.TopicDetails,
				"liker_count":          it.Counts,
				"content_ctime":        it.ItemCtime,
				"notify_type":          it.NotifyType,
				"card_type":            it.CardType,
				"card_brief":           it.CardBrief,
				"card_msg_brief":       it.CardMsgBrief,
				"card_story_title":     it.CardStoryTitle,
				"source":               it.Source,
				"event_ts":             it.Ctime,
				"published_at":         now.Unix(),
			}
			if err := publish(payload); err != nil {
				log.Printf("bilibili: publish notify %s error: %v", key, err)
			}
			published++
		}
	}
	for _, fn := range []struct {
		kind string
		get  func() ([]notifyEvent, error)
	}{
		{"reply", fetchReplyEvents},
		{"at", fetchAtEvents},
		{"like", fetchLikeEvents},
		{"sys", fetchSysEvents},
	} {
		evs, err := fn.get()
		if err != nil {
			if !isWAFGate(err) {
				log.Printf("bilibili: notify %s error: %v", fn.kind, err)
				pushError(err)
			}
			continue
		}
		handle(fn.kind, evs)
	}
	return published
}

func notifyKey(e *notifyEvent) string {
	return e.Type + ":" + strconv.FormatInt(e.ID, 10)
}

func sameUnread(a, b *unreadCounts) bool {
	return a != nil && b != nil && a.Reply == b.Reply && a.At == b.At && a.Like == b.Like && a.Total == b.Total
}

// numAnyAsInt tolerantly converts the numeric fields across the msg lists.
func numAnyAsInt(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, true
		}
	case string:
		if i, err := strconv.ParseInt(n, 10, 64); err == nil {
			return i, true
		}
	}
	return 0, false
}
