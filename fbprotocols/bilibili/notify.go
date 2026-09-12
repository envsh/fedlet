package bilibili

// Notifications: unread counts + the reply / @ / like event lists. Needs
// SESSDATA. The unread aggregate is published every round (kind=notify_unread);
// when it changes, the three event lists are pulled and new events published
// individually.

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"
)

const (
	unreadURL = apiHost + "/x/msg/push-info/unread"
	replyURL  = apiHost + "/x/msg/reply?type=1&page_num=1&page_size=20"
	atURL     = apiHost + "/x/msg/at?type=1&page_size=20&page_num=1"
	likeURL   = apiHost + "/x/msg/like?type=1&page_size=20&page_num=1"
)

// unreadCounts is the shape of /x/msg/push-info/unread. Fields outside the
// aggregate are optional (可能 待实测, tolerant parsing).
type unreadCounts struct {
	Reply int64 `json:"reply"`
	At    int64 `json:"at"`
	Like  int64 `json:"like"`
	// counts for other biz types are ignored; only reply/at/like events are
	// fanned out to detail lists.
	Total int64 `json:"total"`
}

// notifyEvent is a flattened detail-list event shared by reply/at/like.
type notifyEvent struct {
	ID         int64 `json:"id"`
	Type       string
	Ctime      int64
	Uname      string
	Mid        int64
	Message    string
	DynamicURL string
	Subject    string
}

// fetchUnread pulls the aggregate unread counter.
func fetchUnread() (*unreadCounts, error) {
	if jar.get("SESSDATA") == "" {
		return nil, ErrNotLoggedIn
	}
	var data unreadCounts
	if err := getJSON(&data, unreadURL, true); err != nil {
		return nil, err
	}
	return &data, nil
}

// fetchReplyEvents pulls the newest content reply, @ and like events.
func fetchReplyEvents() ([]notifyEvent, error) {
	return fetchNotifyList(replyURL, "reply")
}

func fetchAtEvents() ([]notifyEvent, error) {
	return fetchNotifyList(atURL, "at")
}

func fetchLikeEvents() ([]notifyEvent, error) {
	return fetchNotifyList(likeURL, "like")
}

// fetchNotifyList parses one msg list endpoint defensively: the exact layout
// drifts across reply/at/like (marked 待实测), so it tolerates several field
// spellings and never hard-fails the whole round on a single bad entry.
func fetchNotifyList(u, kind string) ([]notifyEvent, error) {
	if jar.get("SESSDATA") == "" {
		return nil, ErrNotLoggedIn
	}
	body, status, err := doBili("GET", u, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("bilibili: notify %s http %d", kind, status)
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("bilibili: notify %s parse: %w", kind, err)
	}
	if env.Code != 0 {
		return nil, classifyAPIError(u, env.Code, "")
	}
	events := make([]notifyEvent, 0, len(env.Data.Items))
	for _, m := range env.Data.Items {
		ev := notifyEvent{Type: kind}
		if id, ok := numAnyAsInt(m["id"]); ok {
			ev.ID = id
		}
		if c, ok := numAnyAsInt(m["ctime"]); ok {
			ev.Ctime = c
		}
		if s, ok := m["dynamic_url"].(string); ok {
			ev.DynamicURL = s
		}
		extractActor := func(k string) {
			if ua, ok := m[k].(map[string]any); ok {
				if s, ok := ua["uname"].(string); ok && ev.Uname == "" {
					ev.Uname = s
				}
				if v, ok := numAnyAsInt(ua["mid"]); ok && ev.Mid == 0 {
					ev.Mid = v
				}
			}
		}
		switch kind {
		case "reply":
			if r, ok := m["reply"].(map[string]any); ok {
				if c, ok := r["content"].(map[string]any); ok {
					ev.Message, _ = c["message"].(string)
				}
				if a, ok := r["member"].(map[string]any); ok {
					if s, ok := a["uname"].(string); ok && ev.Uname == "" {
						ev.Uname = s
					}
					if v, ok := numAnyAsInt(a["mid"]); ok && ev.Mid == 0 {
						ev.Mid = v
					}
				}
			}
			ev.Subject, _ = m["subject"].(string)
			if p, ok := m["preview"].(map[string]any); ok {
				if s, ok := p["uname"].(string); ok {
					ev.Subject = s
				}
			}
		case "at":
			extractActor("user")
			extractActor("up")
			ev.Message, _ = m["message"].(string)
			ev.Subject, _ = m["video_title"].(string)
		case "like":
			extractActor("user")
			extractActor("up")
			ev.Message = "赞了你的内容"
			ev.Subject, _ = m["video_title"].(string)
			if ev.Subject == "" {
				ev.Subject, _ = m["subject"].(string)
			}
		}
		events = append(events, ev)
	}
	return events, nil
}

// notifyLastUnread remembers the last-published aggregate so rounds report
// changes without spamming.
var notifyLastUnread *unreadCounts

// notifyRound publishes the aggregate + new detail events. Returns the count
// of detail events published.
func notifyRound(state *biliState) int {
	un, err := fetchUnread()
	if err != nil {
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
		for _, fn := range []func() ([]notifyEvent, error){fetchReplyEvents, fetchAtEvents, fetchLikeEvents} {
			evs, err := fn()
			if err != nil {
				log.Printf("bilibili: notify seed error: %v", err)
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
				"kind":         "notify_event",
				"event_type":   kind,
				"id":           it.ID,
				"actor":        it.Uname,
				"actor_mid":    it.Mid,
				"message":      it.Message,
				"subject":      it.Subject,
				"url":          it.DynamicURL,
				"event_ts":     it.Ctime,
				"published_at": now.Unix(),
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
	} {
		evs, err := fn.get()
		if err != nil {
			log.Printf("bilibili: notify %s error: %v", fn.kind, err)
			pushError(err)
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
