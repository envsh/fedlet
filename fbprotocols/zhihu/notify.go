package zhihu

// Zhihu notifications ("通知事件": being upvoted, commented, followed…).
//
// Endpoint (待实测): the logged-in notification stream:
//
//	GET /api/v3/notifications?limit=20&offset=0
//
// Requires a valid z_c0 session. New entries are published incrementally by id.

import (
	"encoding/json"
	"fmt"
)

const notifyURL = "https://www.zhihu.com/api/v3/notifications?limit=20&offset=0"

// NotifyItem is one notification event. Actor, target and verb carry the
// actual content; shapes vary so they are kept as raw JSON.
type NotifyItem struct {
	ID          int64           `json:"id"`
	Verb        string          `json:"verb"`
	ActionText  string          `json:"action_text"`
	CreatedTime int64           `json:"created_time"`
	IsRead      bool            `json:"is_read"`
	Type        string          `json:"type"`
	Actor       json.RawMessage `json:"actor"`
	Target      json.RawMessage `json:"target"`
}

// NotifyResp is the response envelope of the notification stream.
type NotifyResp struct {
	Data []NotifyItem `json:"data"`
}

// FetchNotifications fetches the latest notification events (needs a session).
func FetchNotifications() (*NotifyResp, error) {
	var r NotifyResp
	if err := getJSON(&r, notifyURL, true); err != nil {
		return nil, fmt.Errorf("zhihu: notifications: %w", err)
	}
	return &r, nil
}

// ActorName extracts a readable actor name from a raw actor object.
func ActorName(raw json.RawMessage) string {
	var m map[string]any
	if len(raw) == 0 {
		return ""
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	if s, ok := m["name"].(string); ok {
		return s
	}
	if s, ok := m["url_token"].(string); ok {
		return s
	}
	return ""
}
