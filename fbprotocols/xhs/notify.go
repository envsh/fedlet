package xhs

// Notification streams (requires a real logged-in session; guest sessions
// answer -100/未登录 which the orchestrator treats as needing login).
//
// GET /api/sns/web/unread_count                 -> unread summary
// GET /api/sns/web/v1/you/mentions?num&cursor   -> comments/@ (data.xhs_item)
// GET /api/sns/web/v1/you/likes?num&cursor      -> likes/favorites
// GET /api/sns/web/v1/you/connections?num&cursor-> new followers
//
// Endpoint paths verified from the jackwener client (2026-09). The item
// payloads are loosely structured (comment/like events embed a note + actor);
// parsing is intentionally tolerant and surfaces what is available. 待实测:
// exact item fields may drift with client updates.

import "fmt"

const (
	unreadCountURL = "/api/sns/web/unread_count"
	youBaseURL     = "/api/sns/web/v1/you/"
)

// NotificationKind groups the three /you streams.
type NotificationKind string

const (
	KindMentions    NotificationKind = "mentions"
	KindLikes       NotificationKind = "likes"
	KindConnections NotificationKind = "connections"
)

// NotificationItem is one notification event, flattened from the payload.
type NotificationItem struct {
	ID         string
	Kind       NotificationKind
	ActorName  string
	ActorID    string
	Text       string
	NoteID     string
	NoteTitle  string
	CreateTime int64
	Unread     bool
}

// NotificationsResp aggregates one poll of the three streams.
type NotificationsResp struct {
	Items  []NotificationItem
	Unread int64
}

// FetchNotifications fetches unread summary + the three /you streams.
func FetchNotifications() (*NotificationsResp, error) {
	resp := &NotificationsResp{}
	c := client()

	out, _, err := c.getJSON(unreadCountURL, nil)
	if err == nil {
		if v, ok := numAsInt(out["unread_count"]); ok {
			resp.Unread = v
		} else if v, ok := numAsInt(out["unread"]); ok {
			resp.Unread = v
		}
	}

	for _, kind := range []NotificationKind{KindMentions, KindLikes, KindConnections} {
		items, err := fetchYouKind(c, kind)
		if err != nil {
			logPrefix("notify %s: %v", kind, err)
			continue
		}
		resp.Items = append(resp.Items, items...)
	}
	return resp, nil
}

func fetchYouKind(c *xhsClient, kind NotificationKind) ([]NotificationItem, error) {
	out, _, err := c.getJSON(youBaseURL+string(kind), []kvParam{
		{key: "num", val: "50"},
		{key: "cursor", val: ""},
	})
	if err != nil {
		return nil, err
	}
	return parseYouKind(out, kind), nil
}

func parseYouKind(out map[string]any, kind NotificationKind) []NotificationItem {
	var list []any
	// The web payload stores the list under data.xhs_item; accept any array.
	for _, k := range []string{"xhs_item", "items", "list", "comments"} {
		if l, ok := out[k].([]any); ok {
			list = l
			break
		}
	}
	items := make([]NotificationItem, 0, len(list))
	for _, raw := range list {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		it := NotificationItem{Kind: kind}
		if s, ok := m["id"].(string); ok {
			it.ID = s
		} else if v, ok := numAsInt(m["id"]); ok {
			it.ID = fmt.Sprintf("%d", v)
		}
		if u, ok := m["user"].(map[string]any); ok {
			it.ActorID, _ = u["user_id"].(string)
			it.ActorName, _ = u["nickname"].(string)
		}
		if u, ok := m["from_user"].(map[string]any); ok {
			it.ActorID, _ = u["user_id"].(string)
			it.ActorName, _ = u["nickname"].(string)
		}
		it.Text, _ = m["text"].(string)
		if at, ok := numAsInt(m["create_time"]); ok {
			it.CreateTime = at
		}
		if at, ok := numAsInt(m["created_at"]); ok {
			it.CreateTime = at
		}
		if n, ok := m["note"].(map[string]any); ok {
			it.NoteID, _ = n["id"].(string)
			it.NoteTitle, _ = n["title"].(string)
		}
		if n, ok := m["target"].(map[string]any); ok {
			it.NoteID = firstString(n, "id", "note_id")
			it.NoteTitle = firstString(n, "title", "display_title")
		}
		if v, ok := numAsInt(m["unread"]); ok {
			it.Unread = v == 1
		}
		if v, ok := m["read"].(bool); ok {
			it.Unread = !v
		}
		if it.ID == "" && it.ActorID == "" && it.Text == "" {
			continue
		}
		items = append(items, it)
	}
	return items
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}
