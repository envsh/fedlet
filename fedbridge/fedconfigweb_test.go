package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFedConfigPage(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/fedlet/config", nil)
	w := httptest.NewRecorder()
	handleFedConfig(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"id=\"fedlet-nav\"", "/fedlet/nav.js", "/api/config"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q", want)
		}
	}
}
