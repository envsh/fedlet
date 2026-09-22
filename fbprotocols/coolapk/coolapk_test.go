package coolapk

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"
)

func TestSignTokenGolden(t *testing.T) {
	got := signToken("11111111-1111-4000-8000-111111111111", 1700000000)
	want := "1cb5d082b9c20d16fbc599a88a3bedee11111111-1111-4000-8000-1111111111110x6553f100"
	if got != want {
		t.Fatalf("token mismatch:\n got %s\nwant %s", got, want)
	}
}

func TestFragmentEscapingAlignsWithServer(t *testing.T) {
	want := "%23%2Ffeed%2FdigestList%3Ftype%3D0%2C5%2C9%2C8%2C12%2C10%2C11%2C13%26title%3D%E6%9C%80%E6%96%B0%E5%8A%A8%E6%80%81%26page%3D1"
	got := url.QueryEscape(newsFragment)
	if got != want {
		t.Fatalf("escaping mismatch:\n got %s\nwant %s", got, want)
	}
}

func TestWalkDataFeedAndCardAndSkip(t *testing.T) {
	data := []json.RawMessage{
		json.RawMessage(`{"entityType":"feed","id":"101","message":"x"}`),
		json.RawMessage(`{"entityType":"card","id":"card1","entities":[{"entityType":"feed","id":"201"},{"entityType":"feed","id":"202"}]}`),
		json.RawMessage(`{"entityType":"apk","id":"301"}`),
	}
	raws, ids := walkData(data)
	if len(raws) != 3 || len(ids) != 3 {
		t.Fatalf("want 3 entries, got %d raws %d ids", len(raws), len(ids))
	}
	for i, want := range []string{"101", "201", "202"} {
		if ids[i] != want {
			t.Fatalf("ids[%d]=%s want %s", i, ids[i], want)
		}
	}
}

func TestPublishEntriesDedupeAndFlat(t *testing.T) {
	old := pubfn_
	var published []json.RawMessage
	pubfn_ = func(v any) error { published = append(published, v.(json.RawMessage)); return nil }
	defer func() { pubfn_ = old }()

	raws := []json.RawMessage{
		json.RawMessage(`{"id":"1","a":1}`),
		json.RawMessage(`{"id":"2","b":"x"}`),
		json.RawMessage(`{"id":"1","a":1}`),
	}
	ids := []string{"1", "2", "1"}
	seen := map[string]int64{}
	if n := publishEntries(seen, "news", raws, ids); n != 2 {
		t.Fatalf("first round want 2 new, got %d", n)
	}
	if n := publishEntries(seen, "news", raws, ids); n != 0 {
		t.Fatalf("second round want 0 new, got %d", n)
	}
	if len(published) != 2 {
		t.Fatalf("published want 2, got %d", len(published))
	}
	var m map[string]any
	if err := json.Unmarshal(published[0], &m); err != nil {
		t.Fatal(err)
	}
	if m["proto_type"] != "news" || m["cycle_count"] != float64(3) {
		t.Fatalf("flat fields wrong: %v", m)
	}
	if m["id"] != "1" || m["a"] != float64(1) {
		t.Fatalf("raw fields lost: %v", m)
	}
	if _, dup := m["b"]; dup {
		t.Fatalf("raw field b leaked into first item: %v", m)
	}
}

func TestStateSaveLoadPrune(t *testing.T) {
	p := t.TempDir() + "/coolapk-state.json"
	if s := loadState(p); s.Hotlist == nil || s.News == nil {
		t.Fatal("loadState on missing file must return initialized maps")
	}
	s := newState()
	s.Hotlist["old"] = time.Now().Add(-100 * time.Hour).Unix()
	s.News["fresh"] = time.Now().Unix()
	saveState(p, s)
	s2 := loadState(p)
	if s2.Hotlist["old"] == 0 || s2.News["fresh"] == 0 {
		t.Fatalf("save/load roundtrip lost entries: %+v", s2)
	}
	pruneState(s2, time.Now())
	if _, ok := s2.Hotlist["old"]; ok {
		t.Fatal("expected old entry pruned")
	}
	if _, ok := s2.News["fresh"]; !ok {
		t.Fatal("expected fresh entry kept")
	}
}