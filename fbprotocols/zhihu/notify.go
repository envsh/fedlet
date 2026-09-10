package zhihu

// Zhihu notifications ("通知事件": being upvoted, commented, followed…).
//
// Feed source is the SSR /notifications PAGE, not the API:
//
//	GET /api/v3/notifications?limit=20&offset=0
//
// answers 200 with an HTML shell even for a valid z_c0 with the x-zse-96
// signature — the endpoint is behind a device-session gate (the browser only
// passes because the page's own XHR carries the q_c1/device cookies the SDK
// sets; verified 2026-09 with curl and with the signed client). The same data
// is server-rendered into the page: a plain navigation GET /notifications (no
// signature, no x-requested-with) embeds <script id="js-initialData"> carrying
// initialState.entities.notifications. The pagination endpoint referenced by
// the page (/api/v4/notifications/v2/recent) is itself 302-gated and unused.
//
// Requires a valid z_c0 session. New entries are published incrementally by id.

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// notificationsPageURL is the SSR page the SPA renders the feed from.
const notificationsPageURL = "https://www.zhihu.com/notifications"

// initialDataRE extracts the SPA bootstrap payload out of the page.
var initialDataRE = regexp.MustCompile(`(?s)<script id="js-initialData"[^>]*>(.*?)</script>`)

// htmlTagRE strips inline HTML (comment/answer body markup).
var htmlTagRE = regexp.MustCompile(`(?s)<[^>]*>`)

// NotifyItem is one notification event. The page items carry rendered Chinese
// display text (content.verb) plus the nested actor and target objects.
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

// NotifyResp is the notifications payload handed to the poll loop.
type NotifyResp struct {
	Data []NotifyItem `json:"data"`
}

// notifPageItem is the per-entry shape inside
// initialState.entities.notifications.
type notifPageItem struct {
	ID         flexInt64       `json:"id"`
	Type       string          `json:"type"`
	CreateTime int64           `json:"createTime"`
	IsRead     bool            `json:"isRead"`
	MergeCount int             `json:"mergeCount"`
	Content    notifContent    `json:"content"`
	Target     json.RawMessage `json:"target"`
}

// flexInt64 accepts a number or a numeric string (the live page encodes
// notification ids as strings, e.g. "2079207104758409068").
type flexInt64 int64

// UnmarshalJSON accepts a JSON number, a numeric string, or null.
func (f *flexInt64) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*f = 0
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err == nil {
		*f = flexInt64(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("flexInt64 wants number or numeric string: %s", b)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("flexInt64 %q: %w", s, err)
	}
	*f = flexInt64(n)
	return nil
}

// notifContent is the parsed page item content envelope.
type notifContent struct {
	Verb   string          `json:"verb"`
	Actors json.RawMessage `json:"actors"`
}

// initialDataDoc is the relevant slice of <script id="js-initialData">.
type initialDataDoc struct {
	InitialState struct {
		Entities struct {
			Notifications map[string]notifPageItem `json:"notifications"`
		} `json:"entities"`
	} `json:"initialState"`
}

// FetchNotifications fetches the latest notification events (needs a session).
func FetchNotifications() (*NotifyResp, error) {
	body, status, err := fetchNotificationsPage()
	if err != nil {
		return nil, fmt.Errorf("zhihu: notifications: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("zhihu: notifications: http %d %s", status, truncate(string(body), 200))
	}
	items, err := parseNotificationsPage(body)
	if err != nil {
		return nil, fmt.Errorf("zhihu: notifications: %w", err)
	}
	return &NotifyResp{Data: items}, nil
}

// fetchNotificationsPage runs a plain navigation GET of the /notifications
// page: only the browser headers and the shared cookie jar, no x-zse-96
// signature and no x-requested-with (that device-session gate is exactly what
// the old /api/v3/notifications endpoint denied us). Session / risk-control
// statuses are classified like getRawRef.
func fetchNotificationsPage() ([]byte, int, error) {
	waitRateGate()
	if !sess.hasZ() {
		return nil, http.StatusUnauthorized, ErrNotLoggedIn
	}
	req, err := http.NewRequest(http.MethodGet, notificationsPageURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", zhihuWebUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Referer", "https://www.zhihu.com/")
	req.Header.Set("sec-ch-ua", secChUa)
	req.Header.Set("sec-ch-ua-mobile", secChUaMobile)
	req.Header.Set("sec-ch-ua-platform", secChUaPlatform)
	req.Header.Set("sec-fetch-dest", "document")
	req.Header.Set("sec-fetch-mode", "navigate")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("Cookie", sess.cookieHeader())
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	sess.captureCookies(resp.Header)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		markSessionInvalid(fmt.Errorf("zhihu: session rejected (401) %s", notificationsPageURL))
		return body, resp.StatusCode, ErrNotLoggedIn
	case http.StatusForbidden:
		if isRiskControl(body) {
			noteRateLimit()
		}
	}
	return body, resp.StatusCode, nil
}

// parseNotificationsPage pulls the notification entities out of the page
// bootstrap payload and renders each into a NotifyItem, newest first.
func parseNotificationsPage(body []byte) ([]NotifyItem, error) {
	m := initialDataRE.FindSubmatch(body)
	if len(m) < 2 {
		return nil, fmt.Errorf("%w http 200 %s: js-initialData missing: %s",
			errUnverifiable, notificationsPageURL, truncate(string(body), 120))
	}
	var doc initialDataDoc
	if err := json.Unmarshal(m[1], &doc); err != nil {
		return nil, fmt.Errorf("%w %s: js-initialData: %v", errUnverifiable, notificationsPageURL, err)
	}
	clearRateLimit()
	items := make([]NotifyItem, 0, len(doc.InitialState.Entities.Notifications))
	for _, it := range doc.InitialState.Entities.Notifications {
		if it.Type != "notification" {
			continue
		}
		actor := notifActorName(it.Content.Actors)
		items = append(items, NotifyItem{
			ID:          int64(it.ID),
			Verb:        it.Content.Verb,
			ActionText:  notifActionText(it.Content.Verb, actor, it.Target),
			CreatedTime: it.CreateTime,
			IsRead:      it.IsRead,
			Type:        it.Type,
			Actor:       it.Content.Actors,
			Target:      it.Target,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedTime > items[j].CreatedTime })
	return items, nil
}

// notifActorName resolves the (possibly composite) actor field of a page item:
// content.actors is either a list of {name} objects or a single {name} object.
func notifActorName(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var arr []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
		names := make([]string, 0, len(arr))
		for _, a := range arr {
			if a.Name != "" {
				names = append(names, a.Name)
			}
		}
		if len(names) == len(arr) {
			return strings.Join(names, "、")
		}
	}
	var obj struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Name != "" {
		return obj.Name
	}
	return ""
}

// notifActionText composes the renderable line: "actor verb：preview".
func notifActionText(verb, actor string, target json.RawMessage) string {
	s := strings.TrimSpace(strings.Join([]string{actor, verb}, " "))
	if preview := notifPreview(target); preview != "" {
		s += "：" + preview
	}
	return s
}

// notifPreview pulls a plain-text excerpt from the target: the comment body
// (content), the nested answer excerpt, or the question title.
func notifPreview(target json.RawMessage) string {
	var t struct {
		Content string `json:"content"`
		Target  struct {
			Excerpt  string `json:"excerpt"`
			Question struct {
				Title string `json:"title"`
			} `json:"question"`
		} `json:"target"`
	}
	if err := json.Unmarshal(target, &t); err != nil {
		return ""
	}
	p := strings.TrimSpace(t.Content)
	if p == "" {
		p = strings.TrimSpace(t.Target.Excerpt)
	}
	if p == "" {
		p = strings.TrimSpace(t.Target.Question.Title)
	}
	if p == "" {
		return ""
	}
	p = html.UnescapeString(htmlTagRE.ReplaceAllString(p, " "))
	return truncate(strings.Join(strings.Fields(p), " "), 80)
}

// ActorName extracts a readable actor name from a raw actor object (legacy
// /api/v3 shape) or a page item actor field.
func ActorName(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if s := notifActorName(raw); s != "" {
		return s
	}
	var m map[string]any
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
