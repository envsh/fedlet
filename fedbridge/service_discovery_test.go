package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildDiscoveryShape(t *testing.T) {
	adv := buildDiscovery()
	if adv.Type != discoveryType {
		t.Fatalf("type = %q, want %q", adv.Type, discoveryType)
	}
	if adv.Stamp <= 0 {
		t.Fatalf("timestamp = %d, want > 0", adv.Stamp)
	}
	if adv.PeerID == "" && currentPeerID == "" {
		t.Log("note: both simSelf.PeerID and currentPeerID empty (no key); acceptable in test env")
	}
	// 协议注册靠 //go:build <tag> 的 init()，本测试须带 tag 跑（如 -tags hongguo）。
	if len(adv.Services) < 1 {
		t.Fatal("services empty — run with protocol tags (e.g. -tags hongguo)")
	}
	seen := map[string]bool{}
	for _, s := range adv.Services {
		if s.Name == "" {
			t.Fatal("service with empty name")
		}
		if seen[s.Name] {
			t.Fatalf("duplicate service %q", s.Name)
		}
		seen[s.Name] = true
		if v, ok := s.Attrs["cap_send"]; !ok {
			t.Fatalf("service %s missing attrs.cap_send", s.Name)
		} else if _, isBool := v.(bool); !isBool {
			t.Fatalf("service %s attrs.cap_send = %T, want bool", s.Name, v)
		}
		if _, ok := s.Attrs["has_sendfn"]; !ok {
			t.Fatalf("service %s missing attrs.has_sendfn", s.Name)
		}
	}
}

func TestServiceDiscoveryGetAnd405(t *testing.T) {
	adv := buildDiscovery()
	pre := len(adv.Services)

	srv := httptest.NewServer(http.HandlerFunc(handleServiceDiscovery))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/service/discovery")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	var got DiscoveryAdvertisement
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Type != discoveryType {
		t.Fatalf("resp type = %q", got.Type)
	}
	if len(got.Services) != pre {
		t.Fatalf("resp services = %d, want %d", len(got.Services), pre)
	}

	// non-GET must be 405
	p, err := http.Post(srv.URL+"/api/service/discovery", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer p.Body.Close()
	if p.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", p.StatusCode)
	}
}

func TestLastErrShort(t *testing.T) {
	if got := lastErrShort(nil); got != "" {
		t.Fatalf("nil errs = %q, want empty", got)
	}
	empty := []error{}
	if got := lastErrShort(empty); got != "" {
		t.Fatalf("empty errs = %q", got)
	}
	if got := lastErrShort([]error{&dummyErr{"boom"}}); got != "boom" {
		t.Fatalf("single err = %q, want boom", got)
	}
	long := strings.Repeat("x", 200)
	if got := lastErrShort([]error{&dummyErr{long}}); len(got) != 120 {
		t.Fatalf("long err truncated to %d, want 120", len(got))
	}
}

type dummyErr struct{ s string }

func (d *dummyErr) Error() string { return d.s }