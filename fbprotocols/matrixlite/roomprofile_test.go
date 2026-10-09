package matrixlite

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

// withCleanProfiles gives each test an empty cache. The cache is package state,
// so tests would otherwise see each other's rooms.
func withCleanProfiles(t *testing.T, baseURL string) {
	t.Helper()
	profileMu.Lock()
	prevHost, prevProfiles, prevAttempts, prevPending := profileHost, profiles, roomAttempts, pendingRooms
	profileHost, profiles, roomAttempts, pendingRooms = baseURL, map[string]*roomProfile{}, map[string]time.Time{}, map[string]struct{}{}
	profileMu.Unlock()
	t.Cleanup(func() {
		profileMu.Lock()
		profileHost, profiles, roomAttempts, pendingRooms = prevHost, prevProfiles, prevAttempts, prevPending
		profileMu.Unlock()
	})
}

var strp = strptr

func forgetSummarySupport(baseURL string) {
	summarySupportMu.Lock()
	defer summarySupportMu.Unlock()
	delete(summarySupportBy, baseURL)
}

func TestJoinedRoomsSweep(t *testing.T) {
	withCleanProfiles(t, "http://hs")
	noteProfiles(map[string]*roomProfile{"!known:hs": {Name: strp("Known"), NameQuality: nameQualityExplicit}})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/joined_rooms") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"joined_rooms":["!known:hs","!fresh:hs","!stale:hs"]}`))
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, hc: srv.Client(), summaryClient: srv.Client()}
	sweepJoinedRooms(c)

	profileMu.Lock()
	defer profileMu.Unlock()
	if _, ok := pendingRooms["!known:hs"]; ok {
		t.Error("known room must not be re-queued")
	}
	for _, rid := range []string{"!fresh:hs", "!stale:hs"} {
		if _, ok := pendingRooms[rid]; !ok {
			t.Errorf("%s should have been queued", rid)
		}
	}
}

func TestJoinedRoomsErrorNoPanic(t *testing.T) {
	withCleanProfiles(t, "http://hs")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	sweepJoinedRooms(&Client{baseURL: srv.URL, hc: srv.Client(), summaryClient: srv.Client()})
}

func TestEnqueueBackfillDeduplicatesPending(t *testing.T) {
	withCleanProfiles(t, "http://hs")
	for {
		select {
		case <-backfillCh:
		default:
			goto drained
		}
	}
drained:
	EnqueueBackfill("!r:example.com")
	EnqueueBackfill("!r:example.com")
	cnt := 0
	for {
		select {
		case <-backfillCh:
			cnt++
		default:
			if cnt != 1 {
				t.Errorf("expected exactly 1 queued task, got %d", cnt)
			}
			profileMu.Lock()
			delete(pendingRooms, "!r:example.com")
			profileMu.Unlock()
			return
		}
	}
}

func TestEnqueueMemberDeduplicatesPending(t *testing.T) {
	withCleanMembers(t, "http://hs")
	for {
		select {
		case <-backfillCh:
		default:
			goto drained
		}
	}
drained:
	EnqueueMember("@u:example.com", "!r:example.com")
	EnqueueMember("@u:example.com", "!r:example.com")
	cnt := 0
	for {
		select {
		case <-backfillCh:
			cnt++
		default:
			if cnt != 1 {
				t.Errorf("expected exactly 1 queued task, got %d", cnt)
			}
			memberMu.Lock()
			delete(pendingMembers, "@u:example.com")
			memberMu.Unlock()
			return
		}
	}
}

func TestProfileFromStateEvents(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	raws := []json.RawMessage{
		json.RawMessage(`{"type":"m.room.create","state_key":"","content":{"room_version":"11","m.federate":true}}`),
		json.RawMessage(`{"type":"m.room.name","state_key":"","content":{"name":"Project"}}`),
		json.RawMessage(`{"type":"m.room.topic","state_key":"","content":{"topic":"dev talk"}}`),
		json.RawMessage(`{"type":"m.room.avatar","state_key":"","content":{"url":"mxc://hs/avatar"}}`),
		json.RawMessage(`{"type":"m.room.canonical_alias","state_key":"","content":{"alias":"#dev:example.com"}}`),
		// Non-room state must be ignored entirely.
		json.RawMessage(`{"type":"m.room.member","state_key":"@alice:example.com","content":{"displayname":"Alice"}}`),
	}

	p := profileFromStateEvents(raws)
	if p == nil {
		t.Fatal("expected a profile")
	}
	if p.Name == nil || *p.Name != "Project" {
		t.Errorf("name: got %v", p.Name)
	}
	if p.NameQuality != nameQualityExplicit {
		t.Errorf("name quality: got %v", p.NameQuality)
	}
	if p.Topic == nil || *p.Topic != "dev talk" {
		t.Errorf("topic: got %v", p.Topic)
	}
	if p.Avatar == nil || *p.Avatar != "mxc://hs/avatar" {
		t.Errorf("avatar: got %v", p.Avatar)
	}
	if !p.ExplicitAvatar {
		t.Error("avatar should be marked explicit")
	}
	if p.Alias == nil || *p.Alias != "#dev:example.com" {
		t.Errorf("alias: got %v", p.Alias)
	}
	if p.RoomVersion != "11" {
		t.Errorf("room version: got %q", p.RoomVersion)
	}
	if p.Federated == nil || !*p.Federated {
		t.Errorf("federated: got %v", p.Federated)
	}
}

func TestProfileFromStateEventsEmpty(t *testing.T) {
	if p := profileFromStateEvents(nil); p != nil {
		t.Errorf("expected nil for no events, got %+v", p)
	}
	// Only member events: nothing room-level to publish.
	only := []json.RawMessage{
		json.RawMessage(`{"type":"m.room.member","state_key":"@a:b","content":{}}`),
	}
	if p := profileFromStateEvents(only); p != nil {
		t.Errorf("expected nil for member-only state, got %+v", p)
	}
}

// A lower-quality source must never clobber a better one.
func TestNameQualityPrecedence(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	// Alias arrives first, then an explicit name: explicit wins.
	p := &roomProfile{}
	p.applyStateEvent("m.room.canonical_alias", "", map[string]any{"alias": "#dev:hs"})
	if p.NameQuality != nameQualityCanonicalAlias {
		t.Fatalf("alias should set CanonicalAlias quality, got %v", p.NameQuality)
	}
	p.applyStateEvent("m.room.name", "", map[string]any{"name": "Renamed"})
	if p.NameQuality != nameQualityExplicit || *p.Name != "Renamed" {
		t.Errorf("explicit name should win, got %v %v", p.NameQuality, p.Name)
	}

	// A sliding-sync server-resolved name must not beat an explicit name.
	p.applySlidingSummary(strp("#dev:hs"), nil, nil, nil)
	if p.NameQuality != nameQualityExplicit || *p.Name != "Renamed" {
		t.Errorf("resolved name should not override explicit, got %v %v", p.NameQuality, p.Name)
	}
}

// An explicit rename to "" must clear the cached name, falling back to the
// alias, and to nil when there is no alias. This is why the fields are
// pointers: nil and "" mean different things.
func TestExplicitClearName(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	p := &roomProfile{}
	p.applyStateEvent("m.room.canonical_alias", "", map[string]any{"alias": "#dev:hs"})
	p.applyStateEvent("m.room.name", "", map[string]any{"name": "Old"})

	// Clearing the name while an alias exists falls back to the alias.
	p.applyStateEvent("m.room.name", "", map[string]any{"name": ""})
	if p.Name == nil || *p.Name != "#dev:hs" {
		t.Errorf("expected alias fallback, got %v", p.Name)
	}
	if p.NameQuality != nameQualityCanonicalAlias {
		t.Errorf("expected alias quality, got %v", p.NameQuality)
	}

	// With no alias, the name is gone entirely.
	q := &roomProfile{}
	q.applyStateEvent("m.room.name", "", map[string]any{"name": "Old"})
	q.applyStateEvent("m.room.name", "", map[string]any{"name": ""})
	if q.Name != nil || q.NameQuality != nameQualityNil {
		t.Errorf("expected name cleared, got %v %v", q.Name, q.NameQuality)
	}
}

// Only a non-empty value that differs from the cached one may replace it.
func TestMergeSkipsEmptyAndIdenticalValues(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	noteProfiles(map[string]*roomProfile{"!r:hs": {
		Name:        strp("Old"),
		NameQuality: nameQualityExplicit,
		Topic:       strp("dev"),
		Alias:       strp("#dev:hs"),
		Avatar:      strp("mxc://hs/a"),
	}})
	if got := RoomProfile("!r:hs"); got == nil || *got.Name != "Old" {
		t.Fatalf("seed failed: %+v", got)
	}

	// Empty values must leave the cache untouched.
	noteProfiles(map[string]*roomProfile{"!r:hs": {
		Name:  strp(""),
		Topic: strp(""),
		Alias: strp(""),
	}})
	got := RoomProfile("!r:hs")
	if got == nil || got.Name == nil || *got.Name != "Old" {
		t.Errorf("empty name must not degrade the cache, got %v", got.Name)
	}
	if got.Topic == nil || *got.Topic != "dev" {
		t.Errorf("empty topic must not degrade the cache, got %v", got.Topic)
	}
	if got.Alias == nil || *got.Alias != "#dev:hs" {
		t.Errorf("empty alias must not degrade the cache, got %v", got.Alias)
	}
	if got.Avatar == nil || *got.Avatar != "mxc://hs/a" {
		t.Errorf("omitted avatar must not degrade the cache, got %v", got.Avatar)
	}

	// The same value again is not a change either.
	noteProfiles(map[string]*roomProfile{"!r:hs": {Topic: strp("dev")}})
	got = RoomProfile("!r:hs")
	if got.Topic == nil || *got.Topic != "dev" {
		t.Errorf("topic should be unchanged, got %v", got.Topic)
	}
}

func TestMergeAppliesDifferentNonEmptyValues(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	noteProfiles(map[string]*roomProfile{"!r:hs": {
		Name:        strp("Old"),
		NameQuality: nameQualityExplicit,
		Topic:       strp("dev"),
	}})

	noteProfiles(map[string]*roomProfile{"!r:hs": {
		Name:        strp("New"),
		NameQuality: nameQualityExplicit,
		Topic:       strp("release"),
	}})

	got := RoomProfile("!r:hs")
	if got.Name == nil || *got.Name != "New" {
		t.Errorf("name should have been replaced, got %v", got.Name)
	}
	if got.Topic == nil || *got.Topic != "release" {
		t.Errorf("topic should have been replaced, got %v", got.Topic)
	}
}

// A non-empty but lower-quality source still must not win.
func TestMergeKeepsQualityPrecedence(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	noteProfiles(map[string]*roomProfile{"!r:hs": {
		Name:        strp("Explicit"),
		NameQuality: nameQualityExplicit,
	}})
	noteProfiles(map[string]*roomProfile{"!r:hs": {
		Name:        strp("#dev:hs"),
		NameQuality: nameQualityCanonicalAlias,
	}})

	if got := RoomProfile("!r:hs"); got.Name == nil || *got.Name != "Explicit" {
		t.Errorf("alias-quality name must not replace an explicit one, got %v", got.Name)
	}
}

func TestNoteProfilesPrunesStaleRooms(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	noteProfiles(map[string]*roomProfile{"!fresh:hs": {Name: strp("Fresh")}})

	// Seed a room we last saw long ago; noteProfiles itself always stamps
	// LastSeen, so the entry has to be planted directly.
	profileMu.Lock()
	profiles["!stale:hs"] = &roomProfile{Name: strp("Stale"), LastSeen: time.Now().Add(-8 * 24 * time.Hour)}
	profileMu.Unlock()

	noteProfiles(map[string]*roomProfile{"!other:hs": {Name: strp("Other")}})

	if RoomProfile("!fresh:hs") == nil {
		t.Error("fresh room should be kept")
	}
	if RoomProfile("!stale:hs") != nil {
		t.Error("room unseen for over the 7-day TTL should be pruned")
	}
}

func TestRoomProfileReturnsSnapshot(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	noteProfiles(map[string]*roomProfile{"!r:hs": {Name: strp("First")}})
	got := RoomProfile("!r:hs")
	if got == nil {
		t.Fatal("expected profile")
	}
	// Mutating the returned copy must not corrupt the cache.
	*got.Name = "Mutated"
	if again := RoomProfile("!r:hs"); *again.Name != "First" {
		t.Errorf("cache was mutated through the returned pointer: %q", *again.Name)
	}
}

func TestProfileWireUsesSummaryFieldNames(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	fed := false
	noteProfiles(map[string]*roomProfile{"!r:hs": {
		Name:        strp("Project"),
		NameQuality: nameQualityExplicit,
		Avatar:      strp("mxc://hs/a"),
		Topic:       strp("dev"),
		Alias:       strp("#dev:example.com"),
		RoomType:    "m.space",
		RoomVersion: "11",
		Federated:   &fed,
		MemberCount: 7,
	}})

	w := RoomProfile("!r:hs").wire()
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for key, want := range map[string]any{
		"name":               "Project",
		"avatar_url":         "mxc://hs/a",
		"topic":              "dev",
		"canonical_alias":    "#dev:example.com",
		"num_joined_members": float64(7),
		"room_type":          "m.space",
		"room_version":       "11",
	} {
		if got[key] != want {
			t.Errorf("%s: got %v want %v (json=%s)", key, got[key], want, raw)
		}
	}
	// Explicit false must survive serialization, which requires *bool.
	if v, ok := got["federated"]; !ok || v != false {
		t.Errorf("federated: got %v present=%v (json=%s)", v, ok, raw)
	}
	// Bookkeeping must not leak onto the wire.
	for _, key := range []string{"name_quality", "last_seen", "fetched_at", "fetch_failed", "explicit_avatar"} {
		if _, ok := got[key]; ok {
			t.Errorf("internal field %q leaked into wire json: %s", key, raw)
		}
	}
}

func TestProfileFromSlidingRoomPrefersRequiredState(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	joined := 3
	room := slidingRoom{
		Name:        strp("Server Resolved"),
		Topic:       strp("server topic"),
		JoinedCount: &joined,
		RequiredState: []json.RawMessage{
			json.RawMessage(`{"type":"m.room.name","state_key":"","content":{"name":"Raw Name"}}`),
		},
	}

	p := profileFromSlidingRoom(room)
	if p == nil {
		t.Fatal("expected profile")
	}
	if *p.Name != "Raw Name" {
		t.Errorf("required_state should win, got %q", *p.Name)
	}
	if *p.Topic != "server topic" {
		t.Errorf("topic from room summary: got %v", p.Topic)
	}
	if p.MemberCount != 3 {
		t.Errorf("member count: got %d", p.MemberCount)
	}
}

// With no metadata at all we must return nil, not an empty profile, otherwise
// the caller cannot tell "nothing to publish" from "never seen".
func TestProfileFromSlidingRoomEmpty(t *testing.T) {
	room := slidingRoom{Timeline: []json.RawMessage{json.RawMessage(`{"type":"m.room.message"}`)}}
	if p := profileFromSlidingRoom(room); p != nil {
		t.Errorf("expected nil, got %+v", p)
	}
}

// The persisted next_batch means a restarted client is never sent state for
// unchanged rooms again, so metadata has to survive a restart.
func TestProfilesPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	withCleanProfiles(t, "http://hs")

	fed := true
	noteProfiles(map[string]*roomProfile{"!r:hs": {
		Name:        strp("Project"),
		NameQuality: nameQualityExplicit,
		Avatar:      strp("mxc://hs/a"),
		RoomVersion: "11",
		Federated:   &fed,
		MemberCount: 5,
	}})

	s := &State{Server: "http://hs", AccessToken: "tok", DeviceID: "DEV"}
	c := &Client{baseURL: "http://hs"}
	c.SaveSyncState(s)
	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Simulate a fresh process.
	withCleanProfiles(t, "http://hs")
	if RoomProfile("!r:hs") != nil {
		t.Fatal("cache should be empty after reset")
	}

	var loaded State
	if err := loaded.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	c2 := &Client{baseURL: "http://hs"}
	c2.RestoreFromState(&loaded)

	got := RoomProfile("!r:hs")
	if got == nil {
		t.Fatal("profile should be restored")
	}
	if *got.Name != "Project" || *got.Avatar != "mxc://hs/a" {
		t.Errorf("restored fields: %+v", got)
	}
	if got.RoomVersion != "11" || got.MemberCount != 5 || got.Federated == nil || !*got.Federated {
		t.Errorf("restored extras: %+v", got)
	}
}

func TestFetchRoomSummaryMapsFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := "/_matrix/client/v1/room_summary/" + "!r:example.com"; r.URL.Path != want {
			t.Errorf("path: got %q want %q", r.URL.Path, want)
		}
		w.Write([]byte(`{"room_id":"!r:example.com","name":"Project","topic":"dev",` +
			`"avatar_url":"mxc://hs/a","canonical_alias":"#dev:example.com",` +
			`"num_joined_members":12,"room_version":"11"}`))
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, hc: srv.Client(), summaryClient: srv.Client()}
	p, err := c.fetchRoomSummary("!r:example.com")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if p.Name == nil || *p.Name != "Project" {
		t.Errorf("name: %v", p.Name)
	}
	if p.Topic == nil || *p.Topic != "dev" {
		t.Errorf("topic: %v", p.Topic)
	}
	if p.Avatar == nil || *p.Avatar != "mxc://hs/a" {
		t.Errorf("avatar: %v", p.Avatar)
	}
	if p.Alias == nil || *p.Alias != "#dev:example.com" {
		t.Errorf("alias: %v", p.Alias)
	}
	if p.MemberCount != 12 {
		t.Errorf("members: %d", p.MemberCount)
	}
	if p.RoomVersion != "11" {
		t.Errorf("version: %q", p.RoomVersion)
	}
	if p.Federated != nil {
		t.Errorf("summary cannot supply federated, got %v", *p.Federated)
	}
}

func TestFetchRoomSummaryError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"nope"}`))
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, hc: srv.Client(), summaryClient: srv.Client()}
	if _, err := c.fetchRoomSummary("!r:example.com"); err == nil {
		t.Error("expected an error for a 403 response")
	}
}

// tchncs.de answers 404 M_UNRECOGNIZED for /summary; the probe must mark the
// server unsupported so backfills use the /state fallback.
func TestDetectSummarySupportUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"errcode":"M_UNRECOGNIZED","error":"Unrecognized request"}`))
	}))
	defer srv.Close()
	defer forgetSummarySupport(srv.URL)

	c := &Client{baseURL: srv.URL, summaryClient: srv.Client()}
	c.detectSummarySupport()
	if summarySupported(srv.URL) {
		t.Error("expected /summary to be marked unsupported")
	}
}

// A server that implements the route answers 401 before any room lookup; the
// probe must treat that as supported.
func TestDetectSummarySupportSupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"errcode":"M_MISSING_TOKEN","error":"Missing access token"}`))
	}))
	defer srv.Close()
	defer forgetSummarySupport(srv.URL)

	c := &Client{baseURL: srv.URL, summaryClient: srv.Client()}
	c.detectSummarySupport()
	if !summarySupported(srv.URL) {
		t.Error("expected /summary to be marked supported")
	}
}

// A transport error is transient, never a verdict: the memo must keep whatever
// it held before (unknown defaults to supported) so the next connect re-probes.
func TestDetectSummarySupportTransportErrorKeepsVerdict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // subsequent requests fail at the transport layer
	base := srv.URL
	defer forgetSummarySupport(base)

	prev := summarySupported(base)
	c := &Client{baseURL: base, summaryClient: srv.Client()}
	c.detectSummarySupport()
	if got := summarySupported(base); got != prev {
		t.Errorf("transport error changed verdict: before=%v after=%v", prev, got)
	}
}

// The real fix: without /summary, room_profile still gets values, rebuilt from
// the standard /state endpoint plus a joined_members count.
func TestFetchRoomSummaryFallsBackToState(t *testing.T) {
	withCleanProfiles(t, "http://hs")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/room_summary"):
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"errcode":"M_UNRECOGNIZED","error":"Unrecognized request"}`))
		case strings.HasSuffix(r.URL.Path, "/joined_members"):
			w.Write([]byte(`{"joined":{"@a:hs":{},"@b:hs":{},"@c:hs":{}}}`))
		default: // /state
			w.Write([]byte(`[
				{"type":"m.room.create","state_key":"","sender":"@a:hs","content":{"room_version":"11","type":"m.space","m.federate":false}},
				{"type":"m.room.name","state_key":"","sender":"@a:hs","content":{"name":"Project"}},
				{"type":"m.room.topic","state_key":"","sender":"@a:hs","content":{"topic":"dev"}}
			]`))
		}
	}))
	defer srv.Close()
	setSummarySupport(srv.URL, false)
	defer forgetSummarySupport(srv.URL)

	curClient = &Client{baseURL: srv.URL, hc: srv.Client(), summaryClient: srv.Client()}
	defer func() { curClient = nil }()

	backfillOne("!r:example.com")

	p := RoomProfile("!r:example.com")
	if p == nil {
		t.Fatal("no profile after /state fallback")
	}
	if p.Name == nil || *p.Name != "Project" {
		t.Errorf("name: %+v", p.Name)
	}
	if p.RoomType != "m.space" || p.RoomVersion != "11" {
		t.Errorf("create fields: type=%q version=%q", p.RoomType, p.RoomVersion)
	}
	if p.MemberCount != 3 {
		t.Errorf("members: %d", p.MemberCount)
	}
	if p.Federated == nil || *p.Federated {
		t.Errorf("federated: %+v", p.Federated)
	}
}

func TestFetchRoomSummaryPrefersSummaryWhenSupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/state") {
			t.Error("state must not be fetched when /summary works")
		}
		w.Write([]byte(`{"room_id":"!r:example.com","name":"Project","num_joined_members":4}`))
	}))
	defer srv.Close()
	setSummarySupport(srv.URL, true)
	defer forgetSummarySupport(srv.URL)

	c := &Client{baseURL: srv.URL, hc: srv.Client(), summaryClient: srv.Client()}
	p, err := c.fetchRoomSummary("!r:example.com")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if p.Name == nil || *p.Name != "Project" || p.MemberCount != 4 {
		t.Errorf("profile: %+v", p)
	}
}

// EnqueueBackfill runs on the publish path, so it must never block when the
// queue is full.
func TestEnqueueBackfillNeverBlocks(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < backfillQueueSize*4; i++ {
			EnqueueBackfill("!r:example.com")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("EnqueueBackfill blocked")
	}
}

// A failed summary attempt must not be cached as an empty record: that would
// fix the room in place and block every later backfill, leaving room_profile
// permanently empty. The negative result only throttles, so the room is fetched
// again after the cooldown.
func TestBackfillFailureIsNotStuck(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	var hits int32
	var mu sync.Mutex
	countHits := func() int32 { mu.Lock(); defer mu.Unlock(); return hits }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	curClient = &Client{baseURL: srv.URL, hc: srv.Client(), summaryClient: srv.Client()}
	defer func() { curClient = nil }()

	backfillOne("!r:example.com")
	firstHits := countHits()

	p := RoomProfile("!r:example.com")
	if p != nil {
		t.Fatalf("failed fetch must not leave a record, got %+v", p)
	}
	backfillOne("!r:example.com")

	if h := countHits(); h != firstHits {
		t.Errorf("expected the immediate retry to be throttled, got %d -> %d requests", firstHits, h)
	}

	// Past the cooldown the room is fetched again, so a false-empty state heals.
	profileMu.Lock()
	roomAttempts["!r:example.com"] = time.Now().Add(-roomProfileCooldown - time.Millisecond)
	profileMu.Unlock()
	backfillOne("!r:example.com")

	if h := countHits(); h <= firstHits {
		t.Errorf("expected a retry after the cooldown, got %d -> %d requests", firstHits, h)
	}
}

// A 200 summary that carries nothing publishable is treated like a failure: it
// must not be cached as an empty record, or the publish path would emit {} for
// the room indefinitely.
func TestBackfillEmptySummaryIsNotCached(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"room_id":"!r:example.com"}`))
	}))
	defer srv.Close()

	curClient = &Client{baseURL: srv.URL, hc: srv.Client(), summaryClient: srv.Client()}
	defer func() { curClient = nil }()

	backfillOne("!r:example.com")

	if p := RoomProfile("!r:example.com"); p != nil {
		t.Fatalf("empty summary must not be cached, got %+v", p)
	}
}

// RoomProfileForPublish mirrors MemberProfileForPublish's "no key at all"
// contract: unknown, empty and legacy-failed rooms publish nothing, while a
// room with only an alias or a federated flag still does.
func TestRoomProfileForPublishOmitsEmpty(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	if _, ok := RoomProfileForPublish("!r:example.com"); ok {
		t.Error("unknown room must not publish")
	}

	noteProfiles(map[string]*roomProfile{"!r:example.com": {}})
	if p := RoomProfile("!r:example.com"); p != nil {
		t.Fatalf("empty profile must not be cached: %+v", p)
	}
	if _, ok := RoomProfileForPublish("!r:example.com"); ok {
		t.Error("empty cached record must not publish")
	}

	fed := false
	noteProfiles(map[string]*roomProfile{"!r:example.com": {Alias: strp("#dev:hs"), Federated: &fed}})
	w, ok := RoomProfileForPublish("!r:example.com")
	if !ok {
		t.Fatal("alias alone should publish")
	}
	if w.Alias != "#dev:hs" || w.Federated == nil || *w.Federated {
		t.Errorf("wire: %+v", w)
	}
}

// A room we already know is nameless must not be re-fetched at all.
func TestBackfillSkipsRoomsWithVisibleMeta(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	var hits int32
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Write([]byte(`{"name":"Project"}`))
	}))
	defer srv.Close()

	curClient = &Client{baseURL: srv.URL, hc: srv.Client(), summaryClient: srv.Client()}
	defer func() { curClient = nil }()

	noteProfiles(map[string]*roomProfile{"!r:example.com": {Name: strp("Project"), NameQuality: nameQualityExplicit}})
	backfillOne("!r:example.com")

	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Errorf("known room should not be fetched, got %d requests", hits)
	}
}

func TestBackfillStoresSummaryResult(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"room_id":"!r:example.com","name":"Project","num_joined_members":4}`))
	}))
	defer srv.Close()

	curClient = &Client{baseURL: srv.URL, hc: srv.Client(), summaryClient: srv.Client()}
	defer func() { curClient = nil }()

	backfillOne("!r:example.com")

	p := RoomProfile("!r:example.com")
	if p == nil || p.Name == nil || *p.Name != "Project" {
		t.Fatalf("summary not cached: %+v", p)
	}
	if p.FetchedAt.IsZero() {
		t.Error("FetchedAt should be recorded")
	}
	if p.FetchFailed {
		t.Error("FetchFailed should be false")
	}
	if p.MemberCount != 4 {
		t.Errorf("members: %d", p.MemberCount)
	}
}

// The two publish paths must carry identical metadata.
func TestPublishPathsShareProfileSnapshot(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	fed := true
	noteProfiles(map[string]*roomProfile{"!r:example.com": {
		Name:        strp("Project"),
		NameQuality: nameQualityExplicit,
		Avatar:      strp("mxc://hs/a"),
		Topic:       strp("dev"),
		Alias:       strp("#dev:example.com"),
		RoomType:    "m.space",
		Federated:   &fed,
		MemberCount: 9,
	}})

	w, ok := RoomProfileForPublish("!r:example.com")
	if !ok {
		t.Fatal("expected a publishable profile")
	}
	// This mirrors what pollLoop does per event.
	m := map[string]any{
		"type":         "m.room.message",
		"room_id":      "!r:example.com",
		"event_id":     "$e1",
		"sender":       "@alice:example.com",
		"content":      map[string]any{"body": "hi", "msgtype": "m.text"},
		"room_profile": w,
	}

	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var wire fbshared.RoomProfileWire
	if err := json.Unmarshal(top["room_profile"], &wire); err != nil {
		t.Fatalf("wire decode: %v", err)
	}
	um, ok := matrixEventToUnified(m, raw)
	if !ok {
		t.Fatal("expected a unified message")
	}

	if wire.Name != um.ChatName || wire.AvatarURL != um.ChanIcon || wire.Topic != um.ChatTopic {
		t.Errorf("raw vs unified mismatch: %+v vs %+v", wire, um)
	}
	if wire.Alias != um.ChatAlias || wire.RoomType != um.ChatType {
		t.Errorf("alias/type mismatch: %+v vs %+v", wire, um)
	}
	if wire.NumJoinedMembers != um.ChatMembers {
		t.Errorf("members mismatch: %d vs %d", wire.NumJoinedMembers, um.ChatMembers)
	}
	if um.ChatFederated == nil || *um.ChatFederated != *wire.Federated {
		t.Errorf("federated mismatch: %v vs %v", wire.Federated, um.ChatFederated)
	}

	// And the outer JSON key a subscriber would see.
	if _, ok := top["room_profile"]; !ok {
		t.Errorf("raw event is missing room_profile: %s", raw)
	}
}

// room_profile is always present on the wire, even for a room we know nothing
// about: subscribers must be able to rely on the key existing. It ships as an
// empty object until a backfill provides something, and no pending placeholder
// may leak.
func TestPublishAlwaysIncludesRoomProfile(t *testing.T) {
	withCleanProfiles(t, "http://hs")

	m := map[string]any{"type": "m.room.message", "room_id": "!unknown:example.com"}
	w, ok := RoomProfileForPublish("!unknown:example.com")
	if ok {
		t.Fatal("expected no publishable profile for an unknown room")
	}
	m["room_profile"] = w // mirrors pollLoop: the zero wire is still published
	raw, _ := json.Marshal(m)

	var top map[string]json.RawMessage
	json.Unmarshal(raw, &top)
	v, ok := top["room_profile"]
	if !ok {
		t.Errorf("room_profile must always be present: %s", raw)
	}
	var wire fbshared.RoomProfileWire
	if err := json.Unmarshal(v, &wire); err != nil {
		t.Errorf("room_profile must parse as a wire: %s", raw)
	}
	if _, ok := top["pending"]; ok {
		t.Errorf("pending marker must not be published: %s", raw)
	}
}

// hc must outlive the 30s /sync long poll, while the summary client must fail
// fast; a single shared timeout cannot satisfy both.
func TestSummaryClientTimeoutIsSeparate(t *testing.T) {
	c := &Client{hc: &http.Client{Timeout: 67 * time.Second}, summaryClient: &http.Client{Timeout: summaryTimeout}}
	if c.hc.Timeout <= 30*time.Second {
		t.Errorf("hc timeout %v would abort a 30s long poll", c.hc.Timeout)
	}
	if c.summaryClient.Timeout >= c.hc.Timeout {
		t.Errorf("summary client should fail faster than hc, got %v", c.summaryClient.Timeout)
	}
}

// The sync clients built by loginOrRestore must carry a summary client too, or
// the backfill worker would hit a nil client.
func TestLoginOrRestoreBuildsSummaryClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_matrix/client/v3/account/whoami":
			w.Write([]byte(`{"user_id":"@test:example.com"}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	st := &State{Server: srv.URL, AccessToken: "tok", UserID: "@test:example.com", DeviceID: "DEV"}
	c, err := loginOrRestore(srv.URL, "", "", "", st)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if c.summaryClient == nil {
		t.Fatal("restored client has no summaryClient")
	}
	if c.summaryClient.Timeout != summaryTimeout {
		t.Errorf("summary timeout: got %v", c.summaryClient.Timeout)
	}
}

// ClientFromToken is the third constructor and the one token logins go through;
// without a summary client the backfill worker would nil-deref.
func TestClientFromTokenBuildsSummaryClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_matrix/client/v3/versions":
			w.Write([]byte(`{}`))
		case "/_matrix/client/v3/account/whoami":
			w.Write([]byte(`{"user_id":"@test:example.com"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := ClientFromToken(srv.URL, "tok")
	if err != nil {
		t.Fatalf("from token: %v", err)
	}
	if c.summaryClient == nil {
		t.Fatal("token client has no summaryClient; backfill would panic the worker")
	}
	if c.summaryClient.Timeout != summaryTimeout {
		t.Errorf("summary timeout: got %v", c.summaryClient.Timeout)
	}
}

func TestBindProfilesResetsOnHostChange(t *testing.T) {
	withCleanProfiles(t, "http://hs-a")
	noteProfiles(map[string]*roomProfile{"!r:hs": {Name: strp("Project")}})

	bindProfiles("http://hs-a")
	if RoomProfile("!r:hs") == nil {
		t.Error("same host must keep the cache")
	}

	bindProfiles("http://hs-b")
	if RoomProfile("!r:hs") != nil {
		t.Error("host change must reset the cache")
	}
}
