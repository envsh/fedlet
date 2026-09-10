package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

func TestParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter("120"); d != 120*time.Second {
		t.Errorf("delta: got %v, want 120s", d)
	}
	if d := parseRetryAfter("Wed, 21 Oct 2026 07:28:00 GMT"); d <= 0 {
		t.Errorf("date: got %v, want >0", d)
	}
	if d := parseRetryAfter(""); d != 0 {
		t.Errorf("empty: got %v, want 0", d)
	}
	if d := parseRetryAfter("abc"); d != 0 {
		t.Errorf("garbage: got %v, want 0", d)
	}
}

func TestPublishNtfyCountersAndBackoff(t *testing.T) {
	defer func(old string) { ntfyshTopic = old }(ntfyshTopic)
	defer func(old func(*http.Request) (int, string, string, error)) { doNtfyPublishFn = old }(doNtfyPublishFn)
	ntfyshTopic = "test-topic"
	_ = ntfySentOK.Swap(0)
	_ = ntfySentFail.Swap(0)
	_ = ntfySkipped.Swap(0)
	ntfyMu.Lock()
	ntfyRetryAfter = time.Time{}
	ntfyBackoff = ntfyBackoffMin
	ntfyMu.Unlock()

	// 200 => one success, counters correct.
	doNtfyPublishFn = func(*http.Request) (int, string, string, error) { return 200, "", "{}", nil }
	publishNtfy("test", "c", fbshared.UnifiedMessage{MsgID: "m1", Protocol: "test"})
	if ntfySentOK.Load() != 1 || ntfySentFail.Load() != 0 || ntfySkipped.Load() != 0 {
		t.Fatalf("after 200: ok=%d fail=%d skipped=%d", ntfySentOK.Load(), ntfySentFail.Load(), ntfySkipped.Load())
	}

	// 429 without Retry-After => fail++, backoff armed (60s).
	doNtfyPublishFn = func(*http.Request) (int, string, string, error) { return 429, "", `{"code":42901}`, nil }
	publishNtfy("test", "c", fbshared.UnifiedMessage{MsgID: "m2", Protocol: "test"})
	if ntfySentFail.Load() != 1 {
		t.Fatalf("after 429: fail=%d", ntfySentFail.Load())
	}
	ntfyMu.Lock()
	armed := time.Now().Before(ntfyRetryAfter)
	wait := time.Until(ntfyRetryAfter)
	ntfyMu.Unlock()
	if !armed {
		t.Fatal("backoff not armed after 429")
	}
	if wait < 55*time.Second || wait > 65*time.Second {
		t.Fatalf("backoff wait %v, want ~60s", wait)
	}

	// Next call inside the window is skipped and never touches the wire.
	calls := 0
	doNtfyPublishFn = func(*http.Request) (int, string, string, error) { calls++; return 200, "", "{}", nil }
	publishNtfy("test", "c", fbshared.UnifiedMessage{MsgID: "m3", Protocol: "test"})
	if ntfySkipped.Load() != 1 || calls != 0 {
		t.Fatalf("within backoff: skipped=%d wireCalls=%d", ntfySkipped.Load(), calls)
	}

	// Repeated 429 (window cleared) uses the escalated 120s wait, then stores
	// 240s for the next occurrence.
	ntfyMu.Lock()
	ntfyRetryAfter = time.Time{}
	ntfyMu.Unlock()
	doNtfyPublishFn = func(*http.Request) (int, string, string, error) { return 429, "", `{"code":42901}`, nil }
	publishNtfy("test", "c", fbshared.UnifiedMessage{MsgID: "m4", Protocol: "test"})
	ntfyMu.Lock()
	wait = time.Until(ntfyRetryAfter)
	stored := ntfyBackoff
	ntfyMu.Unlock()
	if wait < 115*time.Second || wait > 125*time.Second {
		t.Fatalf("escalated wait %v, want ~120s", wait)
	}
	if stored != 240*time.Second {
		t.Fatalf("stored backoff %v, want 240s", stored)
	}
}

func TestNtfyStatsHandler(t *testing.T) {
	_ = ntfySentOK.Swap(0)
	_ = ntfySentFail.Swap(0)
	_ = ntfySkipped.Swap(0)
	ntfySentOK.Add(3)
	ntfySentFail.Add(1)
	rr := httptest.NewRecorder()
	handleNtfyStats(rr, httptest.NewRequest(http.MethodGet, "/api/ntfystats", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if body["ok"].(float64) != 3 || body["fail"].(float64) != 1 || body["skipped"].(float64) != 0 {
		t.Errorf("body=%s", rr.Body.String())
	}
}
