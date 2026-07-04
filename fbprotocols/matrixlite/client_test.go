package matrixlite

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestParseAuthToken(t *testing.T) {
	tok, u, p := parseAuth("syt_dXNlcg")
	if tok != "syt_dXNlcg" {
		t.Errorf("token: got %q", tok)
	}
	if u != "" || p != "" {
		t.Errorf("expected empty user/pass, got %q %q", u, p)
	}
}

func TestParseAuthUserPass(t *testing.T) {
	tok, u, p := parseAuth("@user:example.com:hunter2")
	if tok != "" {
		t.Errorf("expected empty token, got %q", tok)
	}
	if u != "@user:example.com" {
		t.Errorf("user: got %q", u)
	}
	if p != "hunter2" {
		t.Errorf("password: got %q", p)
	}
}

func TestParseAuthEmpty(t *testing.T) {
	tok, u, p := parseAuth("")
	if tok != "" || u != "" || p != "" {
		t.Errorf("expected empty, got %q %q %q", tok, u, p)
	}
}

func TestParseAuthTrailingColon(t *testing.T) {
	tok, u, p := parseAuth("@user:example.com:pass:word")
	if tok != "" {
		t.Errorf("expected empty token, got %q", tok)
	}
	if u != "@user:example.com:pass" {
		t.Errorf("user: got %q", u)
	}
	if p != "word" {
		t.Errorf("password: got %q", p)
	}
}

func TestMessageJSONRoundtrip(t *testing.T) {
	msg := Message{
		EventID:   "$abc",
		Sender:    "@alice:example.com",
		Body:      "hello",
		MsgType:   "m.text",
		RoomID:    "!room:example.com",
		Timestamp: 1712345678000,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Body != msg.Body {
		t.Errorf("body mismatch")
	}
	if decoded.Sender != msg.Sender {
		t.Errorf("sender mismatch")
	}
}

func TestLoginFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_matrix/client/v3/login":
			w.Write([]byte(`{"user_id":"@test:example.com","access_token":"tok_abc","device_id":"DEVICE"}`))
		case "/_matrix/client/v3/versions":
			w.Write([]byte(`{"unstable_features":{"org.matrix.simplified_msc3575":true}}`))
		default:
			t.Logf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	}))

	c, err := Login(srv.URL, "@test:example.com", "pass")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if c.accessToken != "tok_abc" {
		t.Errorf("token: got %q", c.accessToken)
	}
	if c.userID != "@test:example.com" {
		t.Errorf("user: got %q", c.userID)
	}
	if c.deviceID != "DEVICE" {
		t.Errorf("device: got %q", c.deviceID)
	}
	if !c.useSliding {
		t.Error("expected sliding sync detected as available")
	}
}

func TestLoginFlowNoSlidingSync(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_matrix/client/v3/login":
			w.Write([]byte(`{"user_id":"@test:example.com","access_token":"tok_def","device_id":"DEV2"}`))
		case "/_matrix/client/v3/versions":
			w.Write([]byte(`{"unstable_features":{}}`))
		default:
			t.Logf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	}))

	c, err := Login(srv.URL, "@test:example.com", "pass")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if c.useSliding {
		t.Error("expected sliding sync NOT available")
	}
}

func TestSlidingSyncExtract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_matrix/client/v3/login":
			w.Write([]byte(`{"user_id":"@test:example.com","access_token":"tok","device_id":"DEV"}`))
		case "/_matrix/client/v3/versions":
			w.Write([]byte(`{"unstable_features":{"org.matrix.simplified_msc3575":true}}`))
		case "/_matrix/client/unstable/org.matrix.simplified_msc3575/sync":
			w.Write([]byte(`{"pos":"5","rooms":{"!room:example.com":{"timeline":[{"type":"m.room.message","sender":"@alice:example.com","content":{"body":"hi","msgtype":"m.text"},"event_id":"$e1","origin_server_ts":100}]}}}`))
		default:
			t.Logf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	}))

	c, err := Login(srv.URL, "@test:example.com", "pass")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	events, err := c.Sync(0)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev["type"] != "m.room.message" {
		t.Errorf("type: got %v", ev["type"])
	}
	if ev["sender"] != "@alice:example.com" {
		t.Errorf("sender: got %v", ev["sender"])
	}
	content, _ := ev["content"].(map[string]any)
	if content["body"] != "hi" {
		t.Errorf("body: got %v", content["body"])
	}
	if content["msgtype"] != "m.text" {
		t.Errorf("msgtype: got %v", content["msgtype"])
	}
	if ev["room_id"] != "!room:example.com" {
		t.Errorf("room: got %v", ev["room_id"])
	}
	if c.slidingPos != "5" {
		t.Errorf("pos: got %q", c.slidingPos)
	}
}

func TestNormalSyncExtract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_matrix/client/v3/login":
			w.Write([]byte(`{"user_id":"@test:example.com","access_token":"tok","device_id":"DEV"}`))
		case "/_matrix/client/v3/versions":
			w.Write([]byte(`{"unstable_features":{}}`))
		case "/_matrix/client/v3/sync":
			w.Write([]byte(`{"next_batch":"s2","rooms":{"join":{"!room:example.com":{"timeline":{"events":[{"type":"m.room.message","sender":"@bob:example.com","content":{"body":"hey","msgtype":"m.text"},"event_id":"$e2","origin_server_ts":200}],"limited":false}}}}}`))
		default:
			t.Logf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	}))

	c, err := Login(srv.URL, "@test:example.com", "pass")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	events, err := c.Sync(0)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev["type"] != "m.room.message" {
		t.Errorf("type: got %v", ev["type"])
	}
	if ev["sender"] != "@bob:example.com" {
		t.Errorf("sender: got %v", ev["sender"])
	}
	content, _ := ev["content"].(map[string]any)
	if content["body"] != "hey" {
		t.Errorf("body: got %v", content["body"])
	}
	if content["msgtype"] != "m.text" {
		t.Errorf("msgtype: got %v", content["msgtype"])
	}
	if ev["room_id"] != "!room:example.com" {
		t.Errorf("room: got %v", ev["room_id"])
	}
	if c.nextBatch != "s2" {
		t.Errorf("next_batch: got %q", c.nextBatch)
	}
}

func TestSlidingSyncFallbackToNormal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_matrix/client/v3/login":
			w.Write([]byte(`{"user_id":"@test:example.com","access_token":"tok","device_id":"DEV"}`))
		case "/_matrix/client/v3/versions":
			w.Write([]byte(`{"unstable_features":{"org.matrix.simplified_msc3575":true}}`))
		case "/_matrix/client/unstable/org.matrix.simplified_msc3575/sync":
			w.WriteHeader(http.StatusInternalServerError)
		case "/_matrix/client/v3/sync":
			w.Write([]byte(`{"next_batch":"s2","rooms":{"join":{"!room:example.com":{"timeline":{"events":[{"type":"m.room.message","sender":"@bob:example.com","content":{"body":"fallback","msgtype":"m.text"},"event_id":"$e2","origin_server_ts":200}],"limited":false}}}}}`))
		default:
			t.Logf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	}))

	c, err := Login(srv.URL, "@test:example.com", "pass")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	events, err := c.Sync(0)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event got %d", len(events))
	}
	ev := events[0]
	if ev["type"] != "m.room.message" {
		t.Errorf("type: got %v", ev["type"])
	}
	if ev["sender"] != "@bob:example.com" {
		t.Errorf("sender: got %v", ev["sender"])
	}
	content, _ := ev["content"].(map[string]any)
	if content["body"] != "fallback" {
		t.Errorf("body: got %v", content["body"])
	}
	if content["msgtype"] != "m.text" {
		t.Errorf("msgtype: got %v", content["msgtype"])
	}
	if ev["room_id"] != "!room:example.com" {
		t.Errorf("room: got %v", ev["room_id"])
	}
}

func TestStateRoundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	s := State{
		Server:       "https://matrix.example.com",
		AccessToken:  "syt_token",
		RefreshToken: "v2_refresh",
		UserID:       "@user:example.com",
		DeviceID:     "DEVICE",
		NextBatch:    "s123_456",
		UseSliding:   false,
	}
	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	var loaded State
	if err := loaded.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.AccessToken != s.AccessToken {
		t.Errorf("token: got %q", loaded.AccessToken)
	}
	if loaded.RefreshToken != s.RefreshToken {
		t.Errorf("refresh: got %q", loaded.RefreshToken)
	}
	if loaded.NextBatch != s.NextBatch {
		t.Errorf("next_batch: got %q", loaded.NextBatch)
	}

	path := filepath.Join(dir, ".config", "fedlet", "matrixlite-state.json")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("state file missing: %v", err)
	}
}

func TestStateValid(t *testing.T) {
	s := State{Server: "https://matrix.example.com", AccessToken: "tok"}
	if !s.Valid() {
		t.Error("expected valid")
	}
	s.AccessToken = ""
	if s.Valid() {
		t.Error("expected invalid with empty token")
	}
	s.AccessToken = "tok"
	s.Server = ""
	if s.Valid() {
		t.Error("expected invalid with empty server")
	}
}

func TestStateSaveSync(t *testing.T) {
	client := &Client{
		baseURL:      "https://matrix.example.com",
		accessToken:  "syt_tok",
		refreshToken: "v2_ref",
		userID:       "@u:example.com",
		deviceID:     "DEV",
		nextBatch:    "s5",
		useSliding:   false,
	}

	s := State{Server: "https://matrix.example.com"}
	client.SaveSyncState(&s)
	if s.AccessToken != "syt_tok" {
		t.Errorf("token: got %q", s.AccessToken)
	}
	if s.NextBatch != "s5" {
		t.Errorf("next_batch: got %q", s.NextBatch)
	}
	if s.UseSliding {
		t.Error("expected useSliding=false")
	}
}

func TestStateRestore(t *testing.T) {
	s := State{
		Server:      "https://matrix.example.com",
		AccessToken: "syt_tok",
		UserID:      "@u:example.com",
		DeviceID:    "DEV",
		NextBatch:   "s5",
	}

	client := &Client{
		baseURL: s.Server,
		hc:      &http.Client{Transport: &http.Transport{DisableKeepAlives: true}},
	}
	client.RestoreFromState(&s)
	if client.accessToken != "syt_tok" {
		t.Errorf("token: got %q", client.accessToken)
	}
	if client.nextBatch != "s5" {
		t.Errorf("next_batch: got %q", client.nextBatch)
	}
	if client.userID != "@u:example.com" {
		t.Errorf("user: got %q", client.userID)
	}
}

func TestTokenRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_matrix/client/v3/login":
			w.Write([]byte(`{"user_id":"@test:example.com","access_token":"syt_old","refresh_token":"v2_ref","device_id":"DEV"}`))
		case "/_matrix/client/v3/versions":
			w.Write([]byte(`{"unstable_features":{}}`))
		case "/_matrix/client/v1/refresh":
			w.Write([]byte(`{"access_token":"syt_new","refresh_token":"v2_new_ref","expires_in_ms":86400000}`))
		default:
			t.Logf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	}))

	c, err := Login(srv.URL, "@test:example.com", "pass")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if c.refreshToken != "v2_ref" {
		t.Errorf("refresh_token: got %q", c.refreshToken)
	}
	if err := c.TokenRefresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if c.accessToken != "syt_new" {
		t.Errorf("access_token after refresh: got %q", c.accessToken)
	}
	if c.refreshToken != "v2_new_ref" {
		t.Errorf("refresh_token after refresh: got %q", c.refreshToken)
	}
}

func TestLoginWithRefreshToken(t *testing.T) {
	gotRefreshTok := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_matrix/client/v3/login":
			var body struct {
				RefreshTok bool `json:"refresh_token"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			gotRefreshTok = body.RefreshTok
			w.Write([]byte(`{"user_id":"@test:example.com","access_token":"syt_tok","refresh_token":"v2_ref","device_id":"DEV"}`))
		case "/_matrix/client/v3/versions":
			w.Write([]byte(`{"unstable_features":{}}`))
		default:
			t.Logf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	}))

	c, err := Login(srv.URL, "@test:example.com", "pass")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !gotRefreshTok {
		t.Error("login request missing refresh_token field")
	}
	if c.accessToken != "syt_tok" {
		t.Errorf("token: got %q", c.accessToken)
	}
	if c.refreshToken != "v2_ref" {
		t.Errorf("refresh_token: got %q", c.refreshToken)
	}
}

func TestDiscoverBaseURLBareHostname(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/matrix/client" {
			w.Write([]byte(`{"m.homeserver":{"base_url":"https://matrix.example.com"}}`))
		}
	}))
	defer srv.Close()

	wellKnownHC = srv.Client()
	defer func() { wellKnownHC = nil }()

	u, _ := url.Parse(srv.URL)
	baseURL, err := DiscoverBaseURL(u.Host)
	if err != nil {
		t.Fatalf("DiscoverBaseURL: %v", err)
	}
	if baseURL != "https://matrix.example.com" {
		t.Errorf("expected discovered URL, got %q", baseURL)
	}
}

func TestDiscoverBaseURLFullURL(t *testing.T) {
	baseURL, err := DiscoverBaseURL("https://matrix.example.com")
	if errors.Is(err, ErrWellKnownNotFound) || errors.Is(err, ErrWellKnownNetwork) {
		if baseURL != "https://matrix.example.com" {
			t.Errorf("expected fallback to input, got %q", baseURL)
		}
	} else if err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else {
		t.Logf("unexpected success: %s", baseURL)
	}
}

func TestDiscoverBaseURLNotFound(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	wellKnownHC = srv.Client()
	defer func() { wellKnownHC = nil }()

	u, _ := url.Parse(srv.URL)
	baseURL, err := DiscoverBaseURL(u.Host)
	if !errors.Is(err, ErrWellKnownNotFound) {
		t.Fatalf("expected ErrWellKnownNotFound, got %v", err)
	}
	if baseURL != "https://"+u.Host {
		t.Errorf("expected fallback https://%s, got %q", u.Host, baseURL)
	}
}

func TestDiscoverBaseURLMalformedJSON(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/matrix/client" {
			w.Write([]byte(`not json`))
		}
	}))
	defer srv.Close()

	wellKnownHC = srv.Client()
	defer func() { wellKnownHC = nil }()

	u, _ := url.Parse(srv.URL)
	baseURL, err := DiscoverBaseURL(u.Host)
	if !errors.Is(err, ErrWellKnownMalformed) {
		t.Fatalf("expected ErrWellKnownMalformed, got %v", err)
	}
	if baseURL != "https://"+u.Host {
		t.Errorf("expected fallback https://%s, got %q", u.Host, baseURL)
	}
}

func TestDiscoverBaseURLEmptyBaseURL(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/matrix/client" {
			w.Write([]byte(`{"m.homeserver":{"base_url":""}}`))
		}
	}))
	defer srv.Close()

	wellKnownHC = srv.Client()
	defer func() { wellKnownHC = nil }()

	u, _ := url.Parse(srv.URL)
	baseURL, err := DiscoverBaseURL(u.Host)
	if !errors.Is(err, ErrWellKnownMalformed) {
		t.Fatalf("expected ErrWellKnownMalformed, got %v", err)
	}
	if baseURL != "https://"+u.Host {
		t.Errorf("expected fallback https://%s, got %q", u.Host, baseURL)
	}
}

func TestDiscoverBaseURLAlreadyHasScheme(t *testing.T) {
	baseURL, err := DiscoverBaseURL("https://matrix.example.com:8448")
	if errors.Is(err, ErrWellKnownNotFound) || errors.Is(err, ErrWellKnownNetwork) {
		if baseURL != "https://matrix.example.com:8448" {
			t.Errorf("expected fallback to input, got %q", baseURL)
		}
	} else if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDiscoverBaseURLInvalidInput(t *testing.T) {
	baseURL, err := DiscoverBaseURL("://bad")
	if err == nil {
		t.Error("expected error for invalid URL")
	}
	if baseURL != "://bad" {
		t.Errorf("expected fallback %q, got %q", "://bad", baseURL)
	}
}
