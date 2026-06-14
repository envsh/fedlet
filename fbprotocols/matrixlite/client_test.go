package matrixlite

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfigJSON(t *testing.T) {
	raw := `{"server":"https://matrix.example.com","user":"@alice:example.com","password":"secret"}`
	cfg := parseConfig(raw)
	if cfg.Server != "https://matrix.example.com" {
		t.Errorf("server: got %q", cfg.Server)
	}
	if cfg.User != "@alice:example.com" {
		t.Errorf("user: got %q", cfg.User)
	}
	if cfg.Password != "secret" {
		t.Errorf("password: got %q", cfg.Password)
	}
}

func TestParseConfigDefault(t *testing.T) {
	cfg := parseConfig("")
	if cfg.Server != "http://localhost:8008" {
		t.Errorf("expected default server, got %q", cfg.Server)
	}
}

func TestParseConfigUserPass(t *testing.T) {
	cfg := parseConfig("@user:example.com:hunter2")
	if cfg.User != "@user:example.com" {
		t.Errorf("user: got %q", cfg.User)
	}
	if cfg.Password != "hunter2" {
		t.Errorf("password: got %q", cfg.Password)
	}
}

func TestExtractMessageEvent(t *testing.T) {
	raw := json.RawMessage(`{"type":"m.room.message","sender":"@alice:example.com","content":{"body":"hello","msgtype":"m.text"},"event_id":"$abc","origin_server_ts":1712345678000}`)
	ev := extractMessageEvent("!room:example.com", raw)
	if ev == nil {
		t.Fatal("expected event")
	}
	if ev.Type != "m.room.message" {
		t.Errorf("type: got %q", ev.Type)
	}
	var msg Message
	if err := json.Unmarshal(ev.Data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Sender != "@alice:example.com" {
		t.Errorf("sender: got %q", msg.Sender)
	}
	if msg.Body != "hello" {
		t.Errorf("body: got %q", msg.Body)
	}
	if msg.RoomID != "!room:example.com" {
		t.Errorf("room: got %q", msg.RoomID)
	}
	if msg.MsgType != "m.text" {
		t.Errorf("msgtype: got %q", msg.MsgType)
	}
}

func TestExtractMessageEventNonMessage(t *testing.T) {
	raw := json.RawMessage(`{"type":"m.room.member","sender":"@alice:example.com","content":{"membership":"join"}}`)
	ev := extractMessageEvent("!room:example.com", raw)
	if ev != nil {
		t.Error("expected nil for non-message event")
	}
}

func TestExtractMessageEventEncrypted(t *testing.T) {
	raw := json.RawMessage(`{"type":"m.room.encrypted","sender":"@bob:example.com","content":{"algorithm":"m.megolm.v1.aes-sha2"}}`)
	ev := extractMessageEvent("!room:example.com", raw)
	if ev != nil {
		t.Error("expected nil for encrypted event")
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
	var msg Message
	if err := json.Unmarshal(events[0].Data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Body != "hi" {
		t.Errorf("body: got %q", msg.Body)
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
	var msg Message
	if err := json.Unmarshal(events[0].Data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Body != "hey" {
		t.Errorf("body: got %q", msg.Body)
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
	var msg Message
	if err := json.Unmarshal(events[0].Data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Body != "fallback" {
		t.Errorf("body: got %q", msg.Body)
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
