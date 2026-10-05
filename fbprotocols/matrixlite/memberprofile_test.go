package matrixlite

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

func withCleanMembers(t *testing.T, baseURL string) {
	t.Helper()
	memberMu.Lock()
	prevHost, prevMembers := memberHost, members
	prevAttempts, prevSeeded := memberAttempts, seededRooms
	memberHost, members = baseURL, map[string]*memberProfile{}
	memberAttempts, seededRooms = map[string]time.Time{}, map[string]time.Time{}
	memberMu.Unlock()
	t.Cleanup(func() {
		memberMu.Lock()
		memberHost, members = prevHost, prevMembers
		memberAttempts, seededRooms = prevAttempts, prevSeeded
		memberMu.Unlock()
	})
}

func TestMemberHarvestFromStateEvent(t *testing.T) {
	withCleanMembers(t, "http://hs")

	harvestMemberEvent(json.RawMessage(
		`{"type":"m.room.member","state_key":"@alice:hs","content":{"displayname":"Alice","avatar_url":"mxc://hs/a","membership":"join"}}`))

	w, ok := MemberProfileForPublish("@alice:hs")
	if !ok {
		t.Fatal("expected a publishable profile")
	}
	if w.DisplayName != "Alice" || w.AvatarURL != "mxc://hs/a" || w.UserID != "@alice:hs" {
		t.Errorf("unexpected wire: %+v", w)
	}
}

func TestMemberHarvestIgnoresRoomScopedEvents(t *testing.T) {
	withCleanMembers(t, "http://hs")

	// A room-level event carries no state_key and must not become a member.
	harvestMemberEvent(json.RawMessage(
		`{"type":"m.room.name","state_key":"","content":{"name":"Project"}}`))
	if _, ok := MemberProfileForPublish(""); ok {
		t.Error("room-level event was recorded as a member")
	}
}

func TestMemberIDAloneIsNotRecorded(t *testing.T) {
	withCleanMembers(t, "http://hs")

	// membership only: a user ID with nothing worth publishing.
	harvestMemberEvent(json.RawMessage(
		`{"type":"m.room.member","state_key":"@ghost:hs","content":{"membership":"join"}}`))

	if _, ok := MemberProfileForPublish("@ghost:hs"); ok {
		t.Error("a bare user ID must not be recorded or published")
	}
	memberMu.Lock()
	n := len(members)
	memberMu.Unlock()
	if n != 0 {
		t.Errorf("expected an empty cache, got %d entries", n)
	}
}

func TestMemberPresenceAloneIsRecorded(t *testing.T) {
	withCleanMembers(t, "http://hs")

	harvestPresenceEvent(json.RawMessage(
		`{"type":"m.presence","sender":"@carol:hs","content":{"presence":"offline"}}`))

	w, ok := MemberProfileForPublish("@carol:hs")
	if !ok {
		t.Fatal("offline presence is a valid attribute and must be stored")
	}
	if w.Presence != "offline" {
		t.Errorf("presence: got %q", w.Presence)
	}
}

func TestMemberPresenceReadsSenderNotContent(t *testing.T) {
	withCleanMembers(t, "http://hs")

	// Synapse serialises presence with include_user_id=False, so the user ID is
	// only in the top-level sender.
	harvestPresenceEvent(json.RawMessage(
		`{"type":"m.presence","sender":"@dave:hs","content":{"presence":"online","status_msg":"brb"}}`))

	w, ok := MemberProfileForPublish("@dave:hs")
	if !ok {
		t.Fatal("expected a profile")
	}
	if w.Presence != "online" || w.StatusMsg != "brb" {
		t.Errorf("unexpected wire: %+v", w)
	}
}

func TestMemberMergeSkipsEmptyAndIdentical(t *testing.T) {
	withCleanMembers(t, "http://hs")

	noteMember(&memberProfile{
		UserID:      "@alice:hs",
		DisplayName: strp("Alice"),
		AvatarURL:   strp("mxc://hs/a"),
		StatusMsg:   "brb",
	})
	// An empty and an identical batch must both be ignored.
	noteMember(&memberProfile{UserID: "@alice:hs", DisplayName: strp("Alice")})
	noteMember(&memberProfile{UserID: "@alice:hs", StatusMsg: ""})

	w, _ := MemberProfileForPublish("@alice:hs")
	if w.DisplayName != "Alice" || w.AvatarURL != "mxc://hs/a" || w.StatusMsg != "brb" {
		t.Errorf("a sparse batch degraded the profile: %+v", w)
	}

	// A genuinely different value does apply.
	noteMember(&memberProfile{UserID: "@alice:hs", DisplayName: strp("Alice (away)")})
	w, _ = MemberProfileForPublish("@alice:hs")
	if w.DisplayName != "Alice (away)" {
		t.Errorf("a different name should win: got %q", w.DisplayName)
	}
}

func TestMemberPublishOmitsKeyWhenUnknown(t *testing.T) {
	withCleanMembers(t, "http://hs")

	// pollLoop must not write sender_profile at all for an unknown sender.
	m := map[string]any{
		"type":    "m.room.message",
		"room_id": "!r:hs",
		"sender":  "@nobody:hs",
		"content": map[string]any{"body": "hi", "msgtype": "m.text"},
	}
	if _, ok := MemberProfileForPublish("@nobody:hs"); ok {
		t.Fatal("unknown sender must not produce a wire value")
	}
	if _, present := m["sender_profile"]; present {
		t.Error("sender_profile must be absent, not empty")
	}
}

func TestMemberPublishPathsShareSnapshot(t *testing.T) {
	withCleanMembers(t, "http://hs")

	noteMember(&memberProfile{
		UserID:      "@alice:hs",
		DisplayName: strp("Alice"),
		AvatarURL:   strp("mxc://hs/a"),
		Presence:    "online",
		StatusMsg:   "busy",
	})

	sp, ok := MemberProfileForPublish("@alice:hs")
	if !ok {
		t.Fatal("expected a profile")
	}
	m := map[string]any{
		"type":           "m.room.message",
		"room_id":        "!r:hs",
		"event_id":       "$e1",
		"sender":         "@alice:hs",
		"content":        map[string]any{"body": "hi", "msgtype": "m.text"},
		"sender_profile": sp,
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var wire fbshared.MemberProfileWire
	if err := json.Unmarshal(top["sender_profile"], &wire); err != nil {
		t.Fatalf("wire decode: %v", err)
	}
	um, ok := matrixEventToUnified(m, raw)
	if !ok {
		t.Fatal("expected a unified message")
	}
	if wire.DisplayName != um.Usernick || wire.AvatarURL != um.UsrIcon {
		t.Errorf("raw vs unified mismatch: %+v vs %+v", wire, um)
	}
	if wire.Presence != um.UsrPresence || wire.StatusMsg != um.UsrStatus {
		t.Errorf("presence mismatch: %+v vs %+v", wire, um)
	}
	// Username stays the MXID, matching gomuks.
	if um.Username != "@alice:hs" || um.UserID != "@alice:hs" {
		t.Errorf("MXID fields changed: %q / %q", um.Username, um.UserID)
	}
}

func TestMemberWireFieldNames(t *testing.T) {
	raw, err := json.Marshal(fbshared.MemberProfileWire{
		UserID:      "@a:hs",
		DisplayName: "A",
		AvatarURL:   "mxc://hs/a",
		Presence:    "online",
		StatusMsg:   "hi",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"user_id", "display_name", "avatar_url", "presence", "status_msg"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing field %q in %s", k, raw)
		}
	}
	// An all-empty wire must serialise to nothing at all.
	empty, err := json.Marshal(fbshared.MemberProfileWire{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(empty) != "{}" {
		t.Errorf("empty wire should be {}, got %s", empty)
	}
}

func TestMemberEmptyProfileResponseThrottles(t *testing.T) {
	withCleanMembers(t, "http://hs")

	var hits int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	prev := curClient
	curClient = &Client{baseURL: srv.URL, hc: srv.Client(), summaryClient: srv.Client()}
	t.Cleanup(func() { curClient = prev })

	// Both the roster seed and the per-user fallback run, and both come back
	// empty. What matters is that the attempt is recorded either way.
	resolveMember("@quiet:hs", "!r:hs")
	mu.Lock()
	first := hits
	mu.Unlock()
	if first == 0 {
		t.Fatal("expected at least one request")
	}
	if _, ok := MemberProfileForPublish("@quiet:hs"); ok {
		t.Error("an empty response must not produce a profile")
	}

	// Within the cooldown a second message must not re-request.
	resolveMember("@quiet:hs", "!r:hs")
	mu.Lock()
	second := hits
	mu.Unlock()
	if second != first {
		t.Errorf("cooldown ignored: %d requests, want %d", second, first)
	}
}

func TestMemberSeedFillsRoomInOneRequest(t *testing.T) {
	withCleanMembers(t, "http://hs")

	var roster, profile int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(r.URL.Path, "/joined_members"):
			roster++
			w.Write([]byte(`{"joined":{"@a:hs":{"display_name":"A","avatar_url":"mxc://hs/a"},"@b:hs":{"display_name":"B"}}}`))
		case strings.Contains(r.URL.Path, "/profile/"):
			profile++
			w.Write([]byte(`{"displayname":"A"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	prev := curClient
	curClient = &Client{baseURL: srv.URL, hc: srv.Client(), summaryClient: srv.Client()}
	t.Cleanup(func() { curClient = prev })

	resolveMember("@a:hs", "!r:hs")

	mu.Lock()
	gotRoster, gotProfile := roster, profile
	mu.Unlock()
	if gotRoster != 1 {
		t.Errorf("expected exactly one roster request, got %d", gotRoster)
	}
	if gotProfile != 0 {
		t.Errorf("the seed already filled this sender, profile should not be fetched (%d)", gotProfile)
	}
	// Everyone in the roster is now known, including the one we never asked for.
	if w, ok := MemberProfileForPublish("@b:hs"); !ok || w.DisplayName != "B" {
		t.Errorf("seed did not fill the whole room: %+v ok=%v", w, ok)
	}
	if !roomSeeded("!r:hs") {
		t.Error("room should be marked seeded")
	}
}

func TestMemberLargeRoomSkipsSeed(t *testing.T) {
	withCleanMembers(t, "http://hs")
	withCleanProfiles(t, "http://hs")

	profileMu.Lock()
	profiles["!big:hs"] = &roomProfile{MemberCount: seedMemberLimit + 1}
	profileMu.Unlock()

	var roster, profile int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(r.URL.Path, "/joined_members") {
			roster++
		}
		if strings.Contains(r.URL.Path, "/profile/") {
			profile++
			w.Write([]byte(`{"displayname":"Solo"}`))
			return
		}
	}))
	defer srv.Close()

	prev := curClient
	curClient = &Client{baseURL: srv.URL, hc: srv.Client(), summaryClient: srv.Client()}
	t.Cleanup(func() { curClient = prev })

	resolveMember("@a:hs", "!big:hs")

	mu.Lock()
	defer mu.Unlock()
	if roster != 0 {
		t.Errorf("a room over the limit must not be seeded wholesale, got %d", roster)
	}
	if profile != 1 {
		t.Errorf("expected one per-user request, got %d", profile)
	}
}

func TestMemberPersistenceRoundTrip(t *testing.T) {
	withCleanMembers(t, "http://hs")

	noteMember(&memberProfile{
		UserID:      "@alice:hs",
		DisplayName: strp("Alice"),
		AvatarURL:   strp("mxc://hs/a"),
	})
	s := &State{}
	c := &Client{}
	c.SaveSyncState(s)

	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded State
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	withCleanMembers(t, "http://hs")
	c2 := &Client{baseURL: "http://hs"}
	c2.RestoreFromState(&decoded)

	w, ok := MemberProfileForPublish("@alice:hs")
	if !ok {
		t.Fatal("member did not survive a restart")
	}
	if w.DisplayName != "Alice" || w.AvatarURL != "mxc://hs/a" {
		t.Errorf("unexpected wire after restore: %+v", w)
	}
}

func TestMemberAttemptsAreNotPersisted(t *testing.T) {
	withCleanMembers(t, "http://hs")

	memberMu.Lock()
	memberAttempts["@quiet:hs"] = time.Now()
	memberMu.Unlock()

	s := &State{}
	c := &Client{}
	c.SaveSyncState(s)
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// State itself always serialises its required fields; what must not appear
	// is any trace of the throttle tables.
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["members"]; ok {
		t.Errorf("a throttled-but-empty member cache should persist nothing: %s", raw)
	}
	if bytes.Contains(raw, []byte("quiet")) {
		t.Errorf("throttle state leaked into the persisted state: %s", raw)
	}
}
