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

// nameQuality mirrors gomuks' NameQuality ordering: higher wins, and a lower
// quality source must never overwrite a higher quality one. gomuks' fourth
// level (NameQualityParticipants, deriving "Alice, Bob and 2 others" from the
// member list) is deliberately not implemented: it needs the full member list,
// which would mean turning off lazy_load_members and inflating the state
// payload on large rooms.
type nameQuality int

const (
	nameQualityNil            nameQuality = 0
	nameQualityCanonicalAlias nameQuality = 2
	nameQualityExplicit       nameQuality = 3
)

// roomProfile tracks the room attributes we care about. Pointer fields
// distinguish "this source did not supply the value" (nil) from "explicitly
// cleared" (pointer to ""), which is what gomuks' CheckChangesAndCopyInto
// relies on by testing != nil rather than != "". A room name or avatar being
// removed is a legitimate change that must overwrite the cache.
type roomProfile struct {
	Name           *string     `json:"name,omitempty"`
	NameQuality    nameQuality `json:"name_quality"`
	Avatar         *string     `json:"avatar,omitempty"`
	ExplicitAvatar bool        `json:"explicit_avatar"`
	Topic          *string     `json:"topic,omitempty"`
	Alias          *string     `json:"canonical_alias,omitempty"`
	RoomType       string      `json:"room_type,omitempty"`
	RoomVersion    string      `json:"room_version,omitempty"`
	Federated      *bool       `json:"federated,omitempty"`
	MemberCount    int         `json:"member_count,omitempty"`

	LastSeen    time.Time `json:"last_seen"`
	FetchedAt   time.Time `json:"fetched_at,omitempty"`
	FetchFailed bool      `json:"fetch_failed,omitempty"`
}

const (
	roomProfileCooldown = 10 * time.Minute
	roomProfileTTL      = 7 * 24 * time.Hour
	backfillQueueSize   = 64
	summaryTimeout      = 15 * time.Second
)

// The cache is keyed by room ID and belongs to the account/homeserver rather
// than to a transient Client object, so it lives at package scope. That also
// lets one backfill worker outlive client re-creation on reconnect instead of
// leaking a goroutine per login attempt.
var (
	profileMu   sync.Mutex
	profiles    = map[string]*roomProfile{}
	profileHost string

	backfillOnce sync.Once
	backfillCh   = make(chan backfillTask, backfillQueueSize)
)

// backfillTaskKind distinguishes what a queued task needs to fetch.
type backfillTaskKind int

const (
	taskRoomSummary backfillTaskKind = iota
	taskMember
)

// backfillTask is one unit of lazy fetching. RoomID is a hint only: the member
// cache is flat, but knowing which room an unknown sender came from lets us seed
// that room's roster in a single request instead of one request per user.
type backfillTask struct {
	kind   backfillTaskKind
	id     string
	roomID string
}

// bindProfiles resets the cache when the homeserver changes.
func bindProfiles(baseURL string) {
	profileMu.Lock()
	defer profileMu.Unlock()
	if profileHost != baseURL {
		profileHost = baseURL
		profiles = map[string]*roomProfile{}
	}
}

// RoomProfile returns a copy of the cached profile for a room, or nil when
// unknown. Callers on the publish path use this: an in-memory lookup only,
// never a network call, so it cannot delay publishing, and handing back a copy
// keeps them from observing a concurrent update by the backfill worker.
func RoomProfile(roomID string) *roomProfile {
	if roomID == "" {
		return nil
	}
	profileMu.Lock()
	defer profileMu.Unlock()
	p := profiles[roomID]
	if p == nil {
		return nil
	}
	return p.clone()
}

// clone deep-copies the pointer fields. A shallow struct copy would still alias
// the strings behind Name/Avatar/Topic/Alias, letting a caller overwrite the
// cached value in place.
func (p *roomProfile) clone() *roomProfile {
	cp := *p
	if p.Name != nil {
		cp.Name = strptr(*p.Name)
	}
	if p.Avatar != nil {
		cp.Avatar = strptr(*p.Avatar)
	}
	if p.Topic != nil {
		cp.Topic = strptr(*p.Topic)
	}
	if p.Alias != nil {
		cp.Alias = strptr(*p.Alias)
	}
	if p.Federated != nil {
		f := *p.Federated
		cp.Federated = &f
	}
	return &cp
}

func strptr(s string) *string { return &s }

func profileSnapshot() map[string]*roomProfile {
	profileMu.Lock()
	defer profileMu.Unlock()
	if len(profiles) == 0 {
		return nil
	}
	out := make(map[string]*roomProfile, len(profiles))
	for id, p := range profiles {
		out[id] = p.clone()
	}
	return out
}

func restoreProfiles(in map[string]*roomProfile) {
	if len(in) == 0 {
		return
	}
	profileMu.Lock()
	defer profileMu.Unlock()
	for id, p := range in {
		profiles[id] = p.clone()
	}
}

// noteProfiles folds freshly synced room attributes into the cache. gomuks does
// the equivalent against its SQLite room table; LastSeen replaces its
// mod_timestamp as the pruning signal.
func noteProfiles(src map[string]*roomProfile) {
	if len(src) == 0 {
		return
	}
	now := time.Now()
	profileMu.Lock()
	defer profileMu.Unlock()
	for id, sp := range src {
		if dp := profiles[id]; dp != nil {
			if dp.mergeFrom(sp) {
				dp.FetchFailed = false
			}
			dp.LastSeen = now
			continue
		}
		cp := *sp
		cp.LastSeen = now
		profiles[id] = &cp
	}
	for id, p := range profiles {
		if now.Sub(p.LastSeen) > roomProfileTTL {
			delete(profiles, id)
		}
	}
}

// shouldReplaceString reports whether an attribute from a freshly synced batch
// should replace the cached one. A source that omits the attribute or sends an
// empty value must never degrade what we already publish, and re-reporting the
// value we already hold is not a change at all.
func shouldReplaceString(old, next *string) bool {
	if next == nil || *next == "" {
		return false
	}
	return old == nil || *old != *next
}

// mergeFrom copies the attributes src actually provides into p and reports
// whether anything changed. Ported from gomuks database/room.go
// CheckChangesAndCopyInto, trimmed to the fields we track, and with the
// "non-empty and different wins" rule applied to every string attribute.
func (p *roomProfile) mergeFrom(src *roomProfile) bool {
	changed := false
	if shouldReplaceString(p.Name, src.Name) && src.NameQuality >= p.NameQuality {
		p.Name, p.NameQuality, changed = src.Name, src.NameQuality, true
	}
	if shouldReplaceString(p.Avatar, src.Avatar) {
		p.Avatar, p.ExplicitAvatar, changed = src.Avatar, src.ExplicitAvatar, true
	}
	if shouldReplaceString(p.Topic, src.Topic) {
		p.Topic, changed = src.Topic, true
	}
	if shouldReplaceString(p.Alias, src.Alias) {
		p.Alias, changed = src.Alias, true
	}
	if src.RoomType != "" {
		p.RoomType, changed = src.RoomType, true
	}
	if src.RoomVersion != "" {
		p.RoomVersion, changed = src.RoomVersion, true
	}
	if src.Federated != nil {
		p.Federated, changed = src.Federated, true
	}
	if src.MemberCount > 0 {
		p.MemberCount, changed = src.MemberCount, true
	}
	return changed
}

// wire projects the visible attributes for publication, dropping the
// bookkeeping fields (quality, timestamps, failure flag). Field names follow
// the MSC3266 RoomSummary response, which is the only stable Matrix spec for
// "the set of room attributes". Federated is our one extension: it comes from
// m.room.create and is not returned by /summary.
func (p *roomProfile) wire() fbshared.RoomProfileWire {
	w := fbshared.RoomProfileWire{
		RoomType:         p.RoomType,
		RoomVersion:      p.RoomVersion,
		NumJoinedMembers: p.MemberCount,
	}
	if p.Name != nil {
		w.Name = *p.Name
	}
	if p.Avatar != nil {
		w.AvatarURL = *p.Avatar
	}
	if p.Topic != nil {
		w.Topic = *p.Topic
	}
	if p.Alias != nil {
		w.Alias = *p.Alias
	}
	if p.Federated != nil {
		// Copy so the wire value cannot be mutated through the cached profile.
		v := *p.Federated
		w.Federated = &v
	}
	return w
}

// hasVisibleMeta reports whether the profile carries anything worth
// publishing, so we do not backfill rooms that are legitimately nameless.
func (p *roomProfile) hasVisibleMeta() bool {
	if p == nil {
		return false
	}
	return (p.Name != nil && *p.Name != "") ||
		(p.Avatar != nil && *p.Avatar != "") ||
		(p.Topic != nil && *p.Topic != "")
}

// applyStateEvent folds one state event into p. Ported from gomuks
// pkg/hicli/sync.go (the m.room.create / name / canonical_alias / avatar /
// topic branches). Only room-level events count; anything carrying a
// non-empty state_key, m.room.member included, is ignored.
func (p *roomProfile) applyStateEvent(evType, stateKey string, content map[string]any) bool {
	if stateKey != "" {
		return false
	}
	switch evType {
	case "m.room.create":
		if s, _ := content["type"].(string); s != "" {
			p.RoomType = s
		}
		if s, _ := content["room_version"].(string); s != "" {
			p.RoomVersion = s
		}
		if b, ok := content["m.federate"].(bool); ok {
			v := b
			p.Federated = &v
		}
	case "m.room.name":
		s, _ := content["name"].(string)
		p.applyName(s, nameQualityExplicit)
	case "m.room.canonical_alias":
		s, _ := content["alias"].(string)
		p.applyAlias(s)
	case "m.room.avatar":
		s, _ := content["url"].(string)
		p.Avatar = &s
		p.ExplicitAvatar = true
	case "m.room.topic":
		s, _ := content["topic"].(string)
		p.Topic = &s
	default:
		return false
	}
	return true
}

// applyName records a name of the given quality, falling back to the known
// canonical alias and then to Nil when the name is empty. Mirrors gomuks'
// handling of an empty m.room.name.
func (p *roomProfile) applyName(name string, q nameQuality) {
	if name != "" {
		p.Name, p.NameQuality = &name, q
		return
	}
	if p.Alias != nil && *p.Alias != "" {
		alias := *p.Alias
		p.Name, p.NameQuality = &alias, nameQualityCanonicalAlias
		return
	}
	p.Name, p.NameQuality = nil, nameQualityNil
}

// applyAlias records the canonical alias and, when it is at least as good as
// the name we already hold, also uses it as the room title. gomuks keeps the
// same ordering so a room named only by alias still shows something.
func (p *roomProfile) applyAlias(alias string) {
	p.Alias = &alias
	if alias == "" {
		if p.NameQuality <= nameQualityCanonicalAlias {
			p.Name, p.NameQuality = nil, nameQualityNil
		}
		return
	}
	if p.NameQuality <= nameQualityCanonicalAlias {
		p.Name, p.NameQuality = &alias, nameQualityCanonicalAlias
	}
}

// applySlidingSummary folds the sliding-sync room-level fields into p. The
// server already resolved the name for us (it falls back to the canonical
// alias when there is no m.room.name), so we record it as Explicit and trust
// it instead of re-running the fallback chain.
func (p *roomProfile) applySlidingSummary(name, avatar, topic *string, joined *int) {
	// required_state already gave us the raw events, so it outranks the
	// server-resolved fields; only fill in what it did not supply.
	if p.NameQuality < nameQualityExplicit && name != nil && *name != "" {
		n := *name
		p.Name, p.NameQuality = &n, nameQualityExplicit
	}
	if p.Avatar == nil && avatar != nil {
		a := *avatar
		p.Avatar, p.ExplicitAvatar = &a, true
	}
	if p.Topic == nil && topic != nil {
		t := *topic
		p.Topic = &t
	}
	if joined != nil && *joined > 0 {
		p.MemberCount = *joined
	}
}

type mxStateEvent struct {
	Type     string          `json:"type"`
	StateKey string          `json:"state_key"`
	Content  json.RawMessage `json:"content"`
}

// profileFromStateEvents folds a room's raw state events into one profile.
// A missing state_key is treated as "" because servers omit it for some room
// attributes.
func profileFromStateEvents(raws []json.RawMessage) *roomProfile {
	if len(raws) == 0 {
		return nil
	}
	p := &roomProfile{}
	got := false
	for _, raw := range raws {
		var ev mxStateEvent
		if json.Unmarshal(raw, &ev) != nil || ev.Type == "" {
			continue
		}
		content := map[string]any{}
		if len(ev.Content) > 0 {
			_ = json.Unmarshal(ev.Content, &content)
		}
		if p.applyStateEvent(ev.Type, ev.StateKey, content) {
			got = true
		}
	}
	if !got {
		return nil
	}
	return p
}

// roomSummaryResp mirrors GET /_matrix/client/v3/rooms/{roomIdOrAlias}/summary
// (MSC3266, stable since Matrix v1.5). Note the field names differ from the
// sliding-sync room object: avatar_url / num_joined_members rather than
// avatar / joined_count.
type roomSummaryResp struct {
	RoomID           string `json:"room_id"`
	Name             string `json:"name"`
	Topic            string `json:"topic"`
	AvatarURL        string `json:"avatar_url"`
	CanonicalAlias   string `json:"canonical_alias"`
	NumJoinedMembers int    `json:"num_joined_members"`
	RoomType         string `json:"room_type"`
	RoomVersion      string `json:"room_version"`
}

func (sr roomSummaryResp) toProfile() *roomProfile {
	p := &roomProfile{RoomType: sr.RoomType, RoomVersion: sr.RoomVersion}
	if sr.Name != "" {
		n := sr.Name
		p.Name, p.NameQuality = &n, nameQualityExplicit
	}
	if sr.Topic != "" {
		t := sr.Topic
		p.Topic = &t
	}
	if sr.AvatarURL != "" {
		a := sr.AvatarURL
		p.Avatar, p.ExplicitAvatar = &a, true
	}
	if sr.CanonicalAlias != "" {
		a := sr.CanonicalAlias
		p.Alias = &a
	}
	if sr.NumJoinedMembers > 0 {
		p.MemberCount = sr.NumJoinedMembers
	}
	return p
}

// StartBackfillWorker launches the single room-summary worker. Fetching runs
// here so room metadata never delays publishing: the sync loop only performs a
// non-blocking send. Safe to call repeatedly.
func StartBackfillWorker() {
	backfillOnce.Do(func() { go backfillWorker() })
}

func backfillWorker() {
	for t := range backfillCh {
		switch t.kind {
		case taskRoomSummary:
			backfillOne(t.id)
		case taskMember:
			resolveMember(t.id, t.roomID)
		}
	}
}

// EnqueueBackfill schedules a summary fetch for a room we have no profile for.
// Called from the publish path, so it must never block: when the queue is full
// we drop the room rather than stall message delivery.
func EnqueueBackfill(roomID string) {
	if roomID == "" {
		return
	}
	select {
	case backfillCh <- backfillTask{kind: taskRoomSummary, id: roomID}:
	default:
		log.Printf("matrixlite: backfill queue full, dropping %s", roomID)
	}
}

// EnqueueMember schedules a fetch for a sender we hold no profile for. Like
// EnqueueBackfill it runs on the publish path, so it must never block: the
// message goes out with just the MXID and the name lands on a later one.
func EnqueueMember(userID, roomID string) {
	if userID == "" {
		return
	}
	select {
	case backfillCh <- backfillTask{kind: taskMember, id: userID, roomID: roomID}:
	default:
		log.Printf("matrixlite: backfill queue full, dropping member %s", userID)
	}
}

func backfillOne(roomID string) {
	profileMu.Lock()
	p := profiles[roomID]
	profileMu.Unlock()

	if p != nil {
		// A previous attempt inside the cooldown window means we already know
		// the room has nothing worth showing; retrying per message would hammer
		// the homeserver.
		if !p.FetchedAt.IsZero() && time.Since(p.FetchedAt) < roomProfileCooldown {
			return
		}
		if p.hasVisibleMeta() {
			return
		}
	}

	muClient.Lock()
	cl := curClient
	muClient.Unlock()
	if cl == nil {
		return
	}

	src, err := cl.fetchRoomSummary(roomID)
	now := time.Now()
	if err != nil {
		log.Printf("matrixlite: summary fetch failed for %s: %v", roomID, err)
		profileMu.Lock()
		cp := profiles[roomID]
		if cp == nil {
			cp = &roomProfile{}
			profiles[roomID] = cp
		}
		cp.FetchFailed, cp.FetchedAt = true, now
		profileMu.Unlock()
		return
	}
	src.FetchedAt, src.LastSeen = now, now
	noteProfiles(map[string]*roomProfile{roomID: src})
}

// fetchRoomSummary pulls room metadata for rooms joined before this cache
// existed, or whose state we never saw: the persisted next_batch/sliding_pos
// means a restarted client is never sent state for unchanged rooms again. It
// uses its own timeout-bearing client because Client.hc deliberately has none,
// since a shared timeout would abort the 30s /sync long poll.
func (c *Client) fetchRoomSummary(roomID string) (*roomProfile, error) {
	u := c.baseURL + "/_matrix/client/v3/rooms/" + url.PathEscape(roomID) + "/summary"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)

	hc := c.summaryClient
	if hc == nil {
		// Defensive: a Client built without a summary client must not panic the
		// backfill worker. Falling back to hc costs us the shorter timeout, which
		// is a far better failure mode than taking the process down.
		hc = c.hc
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if err := authErrorFromResponse(resp, raw); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("summary: %v %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var sr roomSummaryResp
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, fmt.Errorf("summary decode: %w: %s", err, string(raw))
	}
	return sr.toProfile(), nil
}
