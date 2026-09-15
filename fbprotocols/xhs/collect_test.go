package xhs

import "testing"

func TestParseCollectNote(t *testing.T) {
	m := map[string]any{
		"note_id":       "64f7d1scd",
		"display_title": "上海探店",
		"type":          "normal",
		"cover":         "http://sns-webpic.xhscdn.com/x",
		"user": map[string]any{
			"nickname": "旅人",
			"user_id":  "u-123456",
		},
		"interact_info": map[string]any{
			"liked_count":     1024.0,
			"collected_count": 66.0,
		},
	}
	n := parseCollectNote(m)
	if n.NoteID != "64f7d1scd" || n.Title != "上海探店" || n.Type != "normal" {
		t.Fatalf("bad collect note: %+v", n)
	}
	if n.Author != "旅人" || n.AuthorID != "u-123456" {
		t.Fatalf("bad collect user: %+v", n)
	}
	if n.LikedCount != 1024 || n.CollectedCount != 66 {
		t.Fatalf("bad interact_info: %+v", n)
	}
	if got := collectKey(&n); got != "64f7d1scd" {
		t.Fatalf("collectKey: got %q", got)
	}
	if got := collectLink(&n); got != "https://www.xiaohongshu.com/explore/64f7d1scd" {
		t.Fatalf("collectLink: got %q", got)
	}
}

func TestParseCollectNoteFallbacks(t *testing.T) {
	n := parseCollectNote(map[string]any{"title": "无封面", "note_id": "id2"})
	if n.Title != "无封面" {
		t.Fatalf("title fallback: got %q", n.Title)
	}
	empty := parseCollectNote(map[string]any{})
	if got := collectLink(&empty); got != "" {
		t.Fatalf("empty collectLink: got %q", got)
	}
}

func TestItoaInt(t *testing.T) {
	if got := itoaInt(0); got != "0" {
		t.Fatalf("itoaInt(0): got %q", got)
	}
	if got := itoaInt(30); got != "30" {
		t.Fatalf("itoaInt(30): got %q", got)
	}
	if got := itoaInt(-5); got != "-5" {
		t.Fatalf("itoaInt(-5): got %q", got)
	}
}

func TestXInt(t *testing.T) {
	if got := xint(map[string]any{"a": "12"}, "a"); got != 12 {
		t.Fatalf("xint string: got %d", got)
	}
	if got := xint(map[string]any{"a": 3.5}, "a"); got != 3 {
		t.Fatalf("xint float: got %d", got)
	}
	if got := xint(map[string]any{}, "a"); got != 0 {
		t.Fatalf("xint missing: got %d", got)
	}
}

func TestHistoryStubKind(t *testing.T) {
	if HistoryKindUnsupported != "xhs_history" {
		t.Fatalf("kind: got %q", HistoryKindUnsupported)
	}
	if err := FetchHistory(); err == nil {
		t.Fatalf("FetchHistory must report unsupported")
	}
}
