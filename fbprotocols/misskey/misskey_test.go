package misskey

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

func TestResolveVisibility(t *testing.T) {
	cases := []struct {
		name    string
		to      string
		extra   *fbshared.SendExtra
		want    string
		wantErr bool
	}{
		{"explicit public", "@a@m.gg", &fbshared.SendExtra{Visibility: "public"}, "public", false},
		{"explicit home", "", &fbshared.SendExtra{Visibility: "home"}, "home", false},
		{"explicit empty to", "", &fbshared.SendExtra{}, "home", false},
		{"legacy to keyword", "followers", nil, "followers", false},
		{"explicit wins over to", "public", &fbshared.SendExtra{Visibility: "followers"}, "followers", false},
		{"default home", "@a@m.gg", nil, "home", false},
		{"invalid explicit", "", &fbshared.SendExtra{Visibility: "local"}, "", true},
		{"invalid to keyword", "local", nil, "home", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveVisibility(c.to, c.extra)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestDeleteNote(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/notes/delete" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		var req map[string]string
		if err := json.Unmarshal(b, &req); err != nil {
			t.Fatalf("bad body %q: %v", string(b), err)
		}
		if req["i"] != "tok" {
			t.Errorf("missing i/token: %+v", req)
		}
		if req["noteId"] != "as7gnsdvzp" {
			t.Errorf("noteId mismatch: %+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	if err := DeleteNote(srv.URL, "tok", "as7gnsdvzp"); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}

	// 配置未就绪时 Redact 报错,不发 HTTP。
	oldHost, oldToken := curHost, curToken
	defer func() { curHost, curToken = oldHost, oldToken }()
	curHost, curToken = "", ""
	if _, err := Redact("@u@m", "as7gnsdvzp", ""); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("expected not-configured error, got %v", err)
	}

	// 空 note id 直接拒。
	if _, err := Redact("", "", ""); err == nil || !strings.Contains(err.Error(), "empty note id") {
		t.Errorf("expected empty-note-id error, got %v", err)
	}
}
