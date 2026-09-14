package bdtieba

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func mkPost(id, floor int64, t int64) Post {
	return Post{ID: id, Floor: int(floor), Time: t}
}

func floors(posts []Post) []int {
	out := make([]int, len(posts))
	for i := range posts {
		out[i] = posts[i].Floor
	}
	return out
}

func ids(posts []Post) []int64 {
	out := make([]int64, len(posts))
	for i := range posts {
		out[i] = posts[i].ID
	}
	return out
}

func TestTrimToLatestNoFilter(t *testing.T) {
	in := []Post{
		mkPost(1, 1, 100),
		mkPost(2, 2, 200),
		mkPost(3, 3, 300),
		mkPost(4, 4, 400),
		mkPost(5, 5, 500),
		mkPost(6, 6, 600),
		mkPost(7, 7, 700),
		mkPost(8, 8, 800),
		mkPost(9, 9, 900),
	}
	out := trimToLatest(in, 0, 5)
	if want := []int{5, 6, 7, 8, 9}; !reflect.DeepEqual(floors(out), want) {
		t.Fatalf("got floors %v want %v", floors(out), want)
	}
}

func TestTrimToLatestBasePIDFilter(t *testing.T) {
	in := []Post{
		mkPost(1, 1, 100),
		mkPost(2, 2, 200),
		mkPost(3, 3, 300), // pid 3 == basePID → 丢弃
		mkPost(5, 5, 500),
		mkPost(7, 7, 700),
		mkPost(9, 9, 900),
	}
	// 基线 pid=3:过滤后剩 5/7/9 → 取全部(不足5),升序。
	out := trimToLatest(in, 3, 5)
	if want := []int{5, 7, 9}; !reflect.DeepEqual(floors(out), want) {
		t.Fatalf("got floors %v want %v", floors(out), want)
	}
	if want := []int64{5, 7, 9}; !reflect.DeepEqual(ids(out), want) {
		t.Fatalf("got ids %v want %v", ids(out), want)
	}
}

func TestTrimToLatestFilterThenTruncate(t *testing.T) {
	in := []Post{
		mkPost(1, 1, 100),
		mkPost(2, 2, 200),
		mkPost(3, 3, 300),
		mkPost(4, 4, 400),
		mkPost(5, 5, 500),
		mkPost(6, 6, 600),
		mkPost(7, 7, 700),
		mkPost(8, 8, 800),
	}
	// 基线 pid=3,过滤后 5 条(4..8),截取 n=3 → 最新 3 条(pid 8,7,6)反转 → 6,7,8。
	out := trimToLatest(in, 3, 3)
	if want := []int64{6, 7, 8}; !reflect.DeepEqual(ids(out), want) {
		t.Fatalf("got ids %v want %v", ids(out), want)
	}
}

func TestTrimToLatestAllFiltered(t *testing.T) {
	in := []Post{
		mkPost(1, 1, 100),
		mkPost(2, 2, 200),
	}
	if out := trimToLatest(in, 3, 5); len(out) != 0 {
		t.Fatalf("expected empty, got %v", floors(out))
	}
}

func TestTrimToLatestEmpty(t *testing.T) {
	if out := trimToLatest(nil, 0, 5); out != nil {
		t.Fatalf("expected nil, got %v", out)
	}
}

func TestThreadSeenRoundTripWithLastPid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s := threadState{
		10: &threadSeen{ReplyNum: 32, Seen: 1700000000, LastPid: 153913333367},
		20: &threadSeen{ReplyNum: 4, Seen: 1700000001, LastPid: 0},
	}
	saveState(path, s)

	got := loadState(path)
	if len(got) != 2 {
		t.Fatalf("got %d entries want 2", len(got))
	}
	first := got[10]
	if first == nil || first.LastPid != 153913333367 || first.ReplyNum != 32 {
		t.Fatalf("tid=10 round-trip mismatch: %+v", first)
	}
	second := got[20]
	if second == nil || second.LastPid != 0 || second.ReplyNum != 4 {
		t.Fatalf("tid=20 round-trip mismatch: %+v", second)
	}
}

func TestOldFormatMigrationLastPidZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// 旧格式:key 含点号,tid.reply_num。
	old := `{"10.32": 1710000000, "20.4": 1710000001}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}

	got := loadState(path)
	if len(got) != 2 {
		t.Fatalf("got %d entries want 2", len(got))
	}
	for tid, e := range got {
		if e.ReplyNum != -1 {
			t.Fatalf("tid=%d ReplyNum=%d want -1", tid, e.ReplyNum)
		}
		if e.LastPid != 0 {
			t.Fatalf("tid=%d LastPid=%d want 0 (migrated, baseline unknown)", tid, e.LastPid)
		}
		if e.Seen == 0 {
			t.Fatalf("tid=%d Seen not preserved", tid)
		}
	}
}

func TestPruneStateKeepsSeenHint(t *testing.T) {
	now := time.Unix(2000000000, 0)
	s := threadState{
		1: &threadSeen{ReplyNum: 1, Seen: now.Unix()},
		2: &threadSeen{ReplyNum: 1, Seen: now.Add(-24 * time.Hour).Unix()},
	}
	pruneState(s, now.Add(-24*time.Hour).Add(dedupeExpiry).Add(time.Second))
	if _, ok := s[1]; !ok {
		t.Fatal("fresh entry pruned")
	}
	if _, ok := s[2]; ok {
		t.Fatal("stale entry kept")
	}
}
