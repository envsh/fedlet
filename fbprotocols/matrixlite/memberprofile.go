package matrixlite

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

const (
	memberCooldown = 10 * time.Minute
	memberTTL      = 7 * 24 * time.Hour
	// seedMemberLimit caps how many members we pull for a room at once. Above
	// it a joined_members response runs to megabytes, so we fall back to
	// per-user /profile requests instead.
	seedMemberLimit = 512
	// memberBodyCap bounds the roster read. A room larger than this cannot
	// decode, and resolveMember then falls back to the single-user path.
	memberBodyCap = 16 << 20
	// memberProfileBodyCap bounds the per-user profile read, which is tiny.
	memberProfileBodyCap = 64 << 10
)

// The member cache belongs to the account/homeserver rather than to a
// transient Client, so it lives at package scope alongside profiles.
var (
	memberMu   sync.Mutex
	members    = map[string]*memberProfile{}
	memberHost string

	// memberAttempts throttles fetches for users whose profile came back
	// empty. Without it, a member who never set a display name would be
	// re-requested on every message they send. Kept separate from members so an
	// empty result still throttles without recording a bare user ID, and kept
	// transient so a restart retries them: they may have set a name since.
	memberAttempts = map[string]time.Time{}

	// seededRooms records rooms we already pulled a roster for, so the first
	// unknown sender in a known room costs nothing.
	seededRooms = map[string]time.Time{}

	// pendingMembers are users already queued for (or being) resolved. The
	// publish path must not re-enqueue the same unknown sender on every
	// message; dedupe keeps the channel from churning on unprofiled users.
	pendingMembers = map[string]struct{}{}
)

// memberProfile is one user's attributes, shared across every room. Matrix
// display names are room-scoped state, but gomuks' usrCache is keyed by user ID
// alone (gomuks_websocket.go:74); we follow that, so m.room.member harvests from
// different rooms overwrite each other and the most recent writer wins.
type memberProfile struct {
	UserID      string    `json:"user_id"`
	DisplayName *string   `json:"display_name,omitempty"`
	AvatarURL   *string   `json:"avatar_url,omitempty"`
	Presence    string    `json:"presence,omitempty"`
	StatusMsg   string    `json:"status_msg,omitempty"`
	LastSeen    time.Time `json:"last_seen"`
	FetchedAt   time.Time `json:"fetched_at,omitempty"`
}

// hasVisibleData reports whether the profile carries anything worth publishing.
// A bare user ID is not a member profile, so we neither store nor emit entries
// that would carry only the ID. Presence counts on its own: a user we only know
// to be online is still worth reporting.
func (p *memberProfile) hasVisibleData() bool {
	if p == nil {
		return false
	}
	return (p.DisplayName != nil && *p.DisplayName != "") ||
		(p.AvatarURL != nil && *p.AvatarURL != "") ||
		p.Presence != "" || p.StatusMsg != ""
}

// clone deep-copies the pointer fields, so a caller cannot mutate the cache
// through the returned profile.
func (p *memberProfile) clone() *memberProfile {
	if p == nil {
		return nil
	}
	c := *p
	if p.DisplayName != nil {
		v := *p.DisplayName
		c.DisplayName = &v
	}
	if p.AvatarURL != nil {
		v := *p.AvatarURL
		c.AvatarURL = &v
	}
	return &c
}

// wire projects the publishable attributes, dropping the bookkeeping fields.
func (p *memberProfile) wire() fbshared.MemberProfileWire {
	w := fbshared.MemberProfileWire{
		UserID:    p.UserID,
		Presence:  p.Presence,
		StatusMsg: p.StatusMsg,
	}
	if p.DisplayName != nil {
		w.DisplayName = *p.DisplayName
	}
	if p.AvatarURL != nil {
		w.AvatarURL = *p.AvatarURL
	}
	return w
}

// mergeFrom copies the attributes src actually provides and reports whether
// anything changed. Reuses shouldReplaceString so members obey the same
// "non-empty and different wins" rule as rooms. last_active_ago is deliberately
// untracked: it changes on every presence update and would defeat the dedup.
func (p *memberProfile) mergeFrom(src *memberProfile) bool {
	changed := false
	if shouldReplaceString(p.DisplayName, src.DisplayName) {
		p.DisplayName, changed = src.DisplayName, true
	}
	if shouldReplaceString(p.AvatarURL, src.AvatarURL) {
		p.AvatarURL, changed = src.AvatarURL, true
	}
	if src.Presence != "" && src.Presence != p.Presence {
		p.Presence, changed = src.Presence, true
	}
	if src.StatusMsg != "" && src.StatusMsg != p.StatusMsg {
		p.StatusMsg, changed = src.StatusMsg, true
	}
	return changed
}

// bindMembers resets the cache when the homeserver changes, mirroring
// bindProfiles.
func bindMembers(baseURL string) {
	memberMu.Lock()
	defer memberMu.Unlock()
	if memberHost != baseURL {
		memberHost = baseURL
		members = map[string]*memberProfile{}
		memberAttempts = map[string]time.Time{}
		seededRooms = map[string]time.Time{}
	}
}

// noteMember folds one harvested profile into the cache. A profile with no
// visible attribute is dropped, so a bare user ID is never recorded.
func noteMember(src *memberProfile) {
	if src == nil || src.UserID == "" {
		return
	}
	if !src.hasVisibleData() {
		return
	}
	src.LastSeen = time.Now()
	memberMu.Lock()
	defer memberMu.Unlock()
	if dp := members[src.UserID]; dp != nil {
		dp.mergeFrom(src)
		dp.LastSeen = src.LastSeen
	} else {
		members[src.UserID] = src.clone()
	}
}

// pruneMembers drops entries unseen for memberTTL. Called with the lock held.
func pruneMembers(now time.Time) {
	for id, p := range members {
		if now.Sub(p.LastSeen) > memberTTL {
			delete(members, id)
		}
	}
	for id, at := range memberAttempts {
		if now.Sub(at) > memberTTL {
			delete(memberAttempts, id)
		}
	}
	for id, at := range seededRooms {
		if now.Sub(at) > memberTTL {
			delete(seededRooms, id)
		}
	}
}

// MemberProfileForPublish returns a snapshot for the publish path. The false
// return is what keeps sender_profile from ever being written as an empty
// object: a member we know nothing about simply gets no key at all.
func MemberProfileForPublish(userID string) (fbshared.MemberProfileWire, bool) {
	if userID == "" {
		return fbshared.MemberProfileWire{}, false
	}
	memberMu.Lock()
	defer memberMu.Unlock()
	p := members[userID]
	if p == nil || !p.hasVisibleData() {
		return fbshared.MemberProfileWire{}, false
	}
	return p.wire(), true
}

func memberSnapshot() map[string]*memberProfile {
	memberMu.Lock()
	defer memberMu.Unlock()
	if len(members) == 0 {
		return nil
	}
	out := make(map[string]*memberProfile, len(members))
	for id, p := range members {
		out[id] = p.clone()
	}
	return out
}

func restoreMembers(in map[string]*memberProfile) {
	if len(in) == 0 {
		return
	}
	memberMu.Lock()
	defer memberMu.Unlock()
	for id, p := range in {
		if p != nil && p.hasVisibleData() {
			members[id] = p.clone()
		}
	}
}

// memberKnown reports whether we already hold a publishable profile.
func memberKnown(userID string) bool {
	_, ok := MemberProfileForPublish(userID)
	return ok
}

// memberAttemptAllowed reports whether a fetch may run now, and records the
// attempt either way so an empty result still throttles.
func memberAttemptAllowed(userID string) bool {
	memberMu.Lock()
	defer memberMu.Unlock()
	if at, ok := memberAttempts[userID]; ok && time.Since(at) < memberCooldown {
		return false
	}
	memberAttempts[userID] = time.Now()
	return true
}

func roomSeeded(roomID string) bool {
	memberMu.Lock()
	defer memberMu.Unlock()
	_, ok := seededRooms[roomID]
	return ok
}

func markRoomSeeded(roomID string, now time.Time) {
	memberMu.Lock()
	defer memberMu.Unlock()
	seededRooms[roomID] = now
}

// ── harvest: free, straight from the sync stream ────────────────────────────

// mxMemberEvent covers both event types we harvest. The presence user ID lives
// in the top-level sender rather than in content, because Synapse serialises
// presence with include_user_id=False (v2_alpha/sync.py encode_presence).
type mxMemberEvent struct {
	Type     string          `json:"type"`
	Sender   string          `json:"sender"`
	StateKey string          `json:"state_key"`
	Content  json.RawMessage `json:"content"`
}

// harvestMemberEvent folds one m.room.member event into the cache. This cannot
// reuse roomProfile.applyStateEvent: that rejects any event carrying a non-empty
// state_key (roomprofile.go), and a member event's state_key IS the user ID.
func harvestMemberEvent(raw json.RawMessage) {
	var ev mxMemberEvent
	if json.Unmarshal(raw, &ev) != nil || ev.Type != "m.room.member" {
		return
	}
	uid := ev.StateKey
	if uid == "" {
		return
	}
	content := map[string]any{}
	if len(ev.Content) > 0 {
		_ = json.Unmarshal(ev.Content, &content)
	}
	p := &memberProfile{UserID: uid}
	if s, _ := content["displayname"].(string); s != "" {
		p.DisplayName = strptr(s)
	}
	if s, _ := content["avatar_url"].(string); s != "" {
		p.AvatarURL = strptr(s)
	}
	noteMember(p)
}

// harvestMemberEvents walks a raw batch and picks out the member events.
func harvestMemberEvents(raws []json.RawMessage) {
	for _, raw := range raws {
		harvestMemberEvent(raw)
	}
}

// harvestPresenceEvent folds one m.presence event into the cache.
func harvestPresenceEvent(raw json.RawMessage) {
	var ev mxMemberEvent
	if json.Unmarshal(raw, &ev) != nil || ev.Type != "m.presence" {
		return
	}
	uid := ev.Sender
	if uid == "" {
		return
	}
	content := map[string]any{}
	if len(ev.Content) > 0 {
		_ = json.Unmarshal(ev.Content, &content)
	}
	p := &memberProfile{UserID: uid}
	if s, _ := content["presence"].(string); s != "" {
		p.Presence = s
	}
	if s, _ := content["status_msg"].(string); s != "" {
		p.StatusMsg = s
	}
	noteMember(p)
}

func harvestPresenceEvents(raws []json.RawMessage) {
	for _, raw := range raws {
		harvestPresenceEvent(raw)
	}
}

// ── fetch: on demand, only for a sender we have never profiled ──────────────

type joinedMember struct {
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
}

// joinedMembersResp mirrors GET /_matrix/client/v3/rooms/{roomId}/joined_members.
// Note the snake_case field names, unlike the m.room.member content, which uses
// displayname.
type joinedMembersResp struct {
	Joined map[string]joinedMember `json:"joined"`
}

type memberProfileResp struct {
	DisplayName string `json:"displayname"`
	AvatarURL   string `json:"avatar_url"`
}

// clearMemberPending drops a user's dedupe flag once the queued task has been
// consumed, so a later message from the same sender can enqueue them again.
func clearMemberPending(userID string) {
	memberMu.Lock()
	delete(pendingMembers, userID)
	memberMu.Unlock()
}

// resolveMember brings one unknown sender's attributes in, preferring a single
// room-wide seed over a per-user fetch. It takes the client the way backfillOne
// does, since the worker outlives any one login.
func resolveMember(userID, roomID string) {
	if userID == "" || memberKnown(userID) {
		return
	}
	defer clearMemberPending(userID)
	// Record the attempt up front: a member who never set a display name would
	// otherwise be re-requested on every message they send.
	if !memberAttemptAllowed(userID) {
		return
	}

	muClient.Lock()
	cl := curClient
	muClient.Unlock()
	if cl == nil {
		return
	}

	now := time.Now()
	if roomID != "" && !roomSeeded(roomID) {
		if roomMemberCount(roomID) > seedMemberLimit {
			// Too large to pull wholesale; ask for just this user instead.
			fetchProfileFor(cl, userID, now)
			return
		}
		joined, err := cl.fetchJoinedMembers(roomID)
		switch {
		case err != nil:
			// Logged and skipped: the attempt timestamp already recorded above
			// throttles this user, and the single-user fallback keeps the room
			// from staying nameless.
			log.Printf("matrixlite: joined_members %s: %v", roomID, err)
		case joined != nil:
			for uid, m := range joined {
				p := &memberProfile{UserID: uid}
				if m.DisplayName != "" {
					p.DisplayName = strptr(m.DisplayName)
				}
				if m.AvatarURL != "" {
					p.AvatarURL = strptr(m.AvatarURL)
				}
				p.FetchedAt = now
				noteMember(p)
			}
			markRoomSeeded(roomID, now)
			pruneMembers(now)
			log.Printf("matrixlite: seeded %d members in room %s", len(joined), roomID)
			// The seed filled this sender in along with everyone else.
			return
		}
	}
	fetchProfileFor(cl, userID, now)
}

func fetchProfileFor(cl *Client, userID string, now time.Time) {
	p, err := cl.fetchMemberProfile(userID)
	if err != nil {
		log.Printf("matrixlite: profile %s: %v", userID, err)
		return
	}
	if !p.hasVisibleData() {
		// The attempt timestamp recorded above already throttles retries.
		return
	}
	p.FetchedAt = now
	noteMember(p)
}

// roomMemberCount reads the count the room summary worker already cached.
func roomMemberCount(roomID string) int {
	profileMu.Lock()
	defer profileMu.Unlock()
	if p := profiles[roomID]; p != nil {
		return p.MemberCount
	}
	return 0
}

// fetchJoinedMembers pulls the whole roster for a room in one request. Like
// fetchRoomSummary it builds its own request against summaryClient rather than
// using doRequest, because hc carries the 67s long-poll timeout.
func (c *Client) fetchJoinedMembers(roomID string) (map[string]joinedMember, error) {
	u := c.baseURL + "/_matrix/client/v3/rooms/" +
		url.PathEscape(roomID) + "/joined_members"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)

	hc := c.summaryClient
	if hc == nil {
		hc = c.hc
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, memberBodyCap))
	if err := authErrorFromResponse(resp, raw); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("joined_members: %v %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var jr joinedMembersResp
	if err := json.Unmarshal(raw, &jr); err != nil {
		return nil, fmt.Errorf("joined_members decode: %w", err)
	}
	if len(jr.Joined) == 0 {
		return nil, nil
	}
	return jr.Joined, nil
}

// fetchMemberProfile pulls one user's global profile. This is the account-wide
// profile rather than the room-scoped one, so a room-specific nickname is lost;
// it is used only in rooms too large to seed.
func (c *Client) fetchMemberProfile(userID string) (*memberProfile, error) {
	u := c.baseURL + "/_matrix/client/v3/profile/" + url.PathEscape(userID)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)

	hc := c.summaryClient
	if hc == nil {
		hc = c.hc
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, memberProfileBodyCap))
	if err := authErrorFromResponse(resp, raw); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("profile: %v %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var pr memberProfileResp
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, fmt.Errorf("profile decode: %w", err)
	}
	p := &memberProfile{UserID: userID}
	if pr.DisplayName != "" {
		p.DisplayName = strptr(pr.DisplayName)
	}
	if pr.AvatarURL != "" {
		p.AvatarURL = strptr(pr.AvatarURL)
	}
	return p, nil
}
