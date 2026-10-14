package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNavJS(t *testing.T) {
	if !strings.Contains(navJS, "fedlet-nav") || !strings.Contains(navJS, "/fedlet/config") {
		t.Fatalf("nav.js missing expected content")
	}
	req := httptest.NewRequest(http.MethodGet, "/fedlet/nav.js", nil)
	w := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("content-type=%q", ct)
	}
}
