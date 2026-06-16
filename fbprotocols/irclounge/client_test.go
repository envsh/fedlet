package irclounge

import (
	"encoding/json"
	"testing"
	"time"
)

const demoServer = "https://demo.thelounge.chat"

func TestDialEio(t *testing.T) {
	eio, err := DialEio(demoServer)
	if err != nil {
		t.Fatalf("DialEio failed: %v", err)
	}
	defer eio.Close()

	if eio.SID() == "" {
		t.Fatal("expected non-empty sid")
	}
	t.Logf("sid=%s pingInterval=%s", eio.SID(), eio.PingInterval())
}

func TestFullConnect(t *testing.T) {
	client, err := Connect(demoServer, "", "")
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Close()

	t.Log("connected to demo server (public mode)")

	select {
	case ev, ok := <-client.Events:
		if !ok {
			t.Fatal("events channel closed unexpectedly")
		}
		t.Logf("event: %s data=%s", ev.Type, string(ev.Data))
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestParseMsgEvent(t *testing.T) {
	data := []byte(`{"msg":{"id":1,"from":{"nick":"testuser","mode":"@"},"text":"hello world","type":"message","time":"2024-01-01T00:00:00Z","self":false,"highlight":false,"hostmask":"testuser@example.com","msgid":"abc123"},"chan":5}`)

	msg, err := ParseMsgEvent(data)
	if err != nil {
		t.Fatalf("ParseMsgEvent failed: %v", err)
	}

	if msg.ID != 1 {
		t.Errorf("expected ID=1, got %d", msg.ID)
	}
	if msg.From == nil || msg.From.Nick != "testuser" {
		t.Errorf("expected from.nick=testuser, got %v", msg.From)
	}
	if msg.Text != "hello world" {
		t.Errorf("expected text=hello world, got %q", msg.Text)
	}
	if msg.Type != MsgMessage {
		t.Errorf("expected type=message, got %s", msg.Type)
	}
}

func TestParseMsgAction(t *testing.T) {
	data := []byte(`{"msg":{"id":2,"from":{"nick":"bot"},"text":"/me waves","type":"action","time":"2024-01-01T00:00:00Z"},"chan":5}`)

	msg, err := ParseMsgEvent(data)
	if err != nil {
		t.Fatalf("ParseMsgEvent failed: %v", err)
	}

	if msg.Type != MsgAction {
		t.Errorf("expected type=action, got %s", msg.Type)
	}
}

func TestParseMsgInvite(t *testing.T) {
	data := []byte(`{"msg":{"id":3,"from":{"nick":"user1"},"text":"","type":"invite","time":"2024-01-01T00:00:00Z","channel":"#secret","invitedYou":true},"chan":5}`)

	msg, err := ParseMsgEvent(data)
	if err != nil {
		t.Fatalf("ParseMsgEvent failed: %v", err)
	}
	if msg.Type != MsgInvite {
		t.Errorf("expected type=invite, got %s", msg.Type)
	}
}

func TestParseMsgNotice(t *testing.T) {
	data := []byte(`{"msg":{"id":4,"from":{"nick":"NickServ"},"text":"This is a notice","type":"notice","time":"2024-01-01T00:00:00Z"},"chan":5}`)

	msg, err := ParseMsgEvent(data)
	if err != nil {
		t.Fatalf("ParseMsgEvent failed: %v", err)
	}
	if msg.Type != MsgNotice {
		t.Errorf("expected type=notice, got %s", msg.Type)
	}
}

func TestParseConfigJSON(t *testing.T) {
	raw := `{"server":"http://lounge.local:8080","user":"alice","password":"secret123"}`
	cfg := parseConfig(raw)

	if cfg.Server != "http://lounge.local:8080" {
		t.Errorf("expected server http://lounge.local:8080, got %s", cfg.Server)
	}
	if cfg.User != "alice" {
		t.Errorf("expected user alice, got %s", cfg.User)
	}
	if cfg.Password != "secret123" {
		t.Errorf("expected password secret123, got %s", cfg.Password)
	}
}

func TestParseConfigUserPass(t *testing.T) {
	raw := "alice:secret123"
	cfg := parseConfig(raw)

	if cfg.Server != "http://localhost:9000" {
		t.Errorf("expected default server, got %s", cfg.Server)
	}
	if cfg.User != "alice" {
		t.Errorf("expected user alice, got %s", cfg.User)
	}
	if cfg.Password != "secret123" {
		t.Errorf("expected password secret123, got %s", cfg.Password)
	}
}

func TestParseConfigEmpty(t *testing.T) {
	cfg := parseConfig("")
	if cfg.Server != "http://localhost:9000" {
		t.Errorf("expected default server, got %s", cfg.Server)
	}
}

func TestParseConfigPartialJSON(t *testing.T) {
	raw := `{"server":"https://irc.example.com"}`
	cfg := parseConfig(raw)

	if cfg.Server != "https://irc.example.com" {
		t.Errorf("expected server https://irc.example.com, got %s", cfg.Server)
	}
	if cfg.User != "" {
		t.Errorf("expected empty user, got %s", cfg.User)
	}
}

func TestEioPacketEncode(t *testing.T) {
	tests := []struct {
		name string
		pkt  EioPacket
		want string
	}{
		{"open", EioPacket{Type: EioOpen, Data: []byte(`{"sid":"x","pingInterval":25000}`)}, `0{"sid":"x","pingInterval":25000}`},
		{"close", EioPacket{Type: EioClose}, "1"},
		{"ping", EioPacket{Type: EioPing}, "2"},
		{"pong", EioPacket{Type: EioPong}, "3"},
		{"message", EioPacket{Type: EioMessage, Data: []byte(`2["msg",{}]`)}, `42["msg",{}]`},
		{"upgrade", EioPacket{Type: EioUpgrade, Data: []byte("websocket")}, "5websocket"},
		{"noop", EioPacket{Type: EioNoop}, "6"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(encodeTextPacket(tt.pkt))
			if got != tt.want {
				t.Errorf("encodeTextPacket got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEioPacketDecode(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []EioPacket
	}{
		{"open", `0{"sid":"x"}`, []EioPacket{{Type: EioOpen, Data: []byte(`{"sid":"x"}`)}}},
		{"ping", "2", []EioPacket{{Type: EioPing}}},
		{"pong", "3", []EioPacket{{Type: EioPong}}},
		{"message", `42["msg",{}]`, []EioPacket{{Type: EioMessage, Data: []byte(`2["msg",{}]`)}}},
		{"ping+message", "2" + string(rune(30)) + `42["msg",{}]`, []EioPacket{{Type: EioPing}, {Type: EioMessage, Data: []byte(`2["msg",{}]`)}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeTextPackets([]byte(tt.input))
			if len(got) != len(tt.want) {
				t.Fatalf("got %d packets, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i].Type != tt.want[i].Type {
					t.Errorf("packet %d: type got %d, want %d", i, got[i].Type, tt.want[i].Type)
				}
				if string(got[i].Data) != string(tt.want[i].Data) {
					t.Errorf("packet %d: data got %q, want %q", i, string(got[i].Data), string(tt.want[i].Data))
				}
			}
		})
	}
}

func TestSioEventParse(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantEvent string
		wantData  string
	}{
		{
			name:      "msg event",
			input:     `2["msg",{"id":1,"from":{"nick":"test"}}]`,
			wantEvent: "msg",
			wantData:  `{"id":1,"from":{"nick":"test"}}`,
		},
		{
			name:      "simple event no data",
			input:     `2["auth:success"]`,
			wantEvent: "auth:success",
			wantData:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := parseSioEvent([]byte(tt.input))
			if err != nil {
				t.Fatalf("parseSioEvent failed: %v", err)
			}
			if ev.Name != tt.wantEvent {
				t.Errorf("got event %q, want %q", ev.Name, tt.wantEvent)
			}
			if string(ev.Data) != tt.wantData {
				t.Errorf("got data %q, want %q", string(ev.Data), tt.wantData)
			}
		})
	}
}

func TestSioEventPacket(t *testing.T) {
	ev, err := parseSioEvent([]byte(`2["test",{"key":"val"}]`))
	if err != nil {
		t.Fatalf("parseSioEvent failed: %v", err)
	}
	if ev.Name != "test" {
		t.Errorf("expected event=test, got %s", ev.Name)
	}
	if string(ev.Data) != `{"key":"val"}` {
		t.Errorf("expected data={\"key\":\"val\"}, got %s", string(ev.Data))
	}
}

func TestChannelType(t *testing.T) {
	if ChanChannel != "channel" {
		t.Errorf("expected ChanChannel=channel, got %s", ChanChannel)
	}
	if ChanLobby != "lobby" {
		t.Errorf("expected ChanLobby=lobby, got %s", ChanLobby)
	}
}

func TestChanState(t *testing.T) {
	if ChanParted != 0 {
		t.Errorf("expected ChanParted=0, got %d", ChanParted)
	}
	if ChanJoined != 1 {
		t.Errorf("expected ChanJoined=1, got %d", ChanJoined)
	}
}

func TestMessageJSONRoundtrip(t *testing.T) {
	msg := Message{
		ID:   1,
		From: &UserInMessage{Nick: "test", Mode: "@"},
		Text: "hello",
		Type: MsgMessage,
		Time: "2024-01-01T00:00:00Z",
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.ID != msg.ID {
		t.Errorf("id mismatch")
	}
	if decoded.From.Nick != msg.From.Nick {
		t.Errorf("nick mismatch")
	}
}

func TestUserInMessageFromJSON(t *testing.T) {
	data := []byte(`{"nick":"alice","mode":"+"}`)
	var u UserInMessage
	if err := json.Unmarshal(data, &u); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if u.Nick != "alice" {
		t.Errorf("expected nick=alice, got %s", u.Nick)
	}
	if u.Mode != "+" {
		t.Errorf("expected mode=+, got %s", u.Mode)
	}
}

func TestChannelJSON(t *testing.T) {
	ch := Channel{
		ID:    1,
		Name:  "#test",
		Topic: "test channel",
		Type:  ChanChannel,
		State: ChanJoined,
	}

	data, err := json.Marshal(ch)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded Channel
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.ID != ch.ID {
		t.Errorf("id mismatch")
	}
	if decoded.Type != ChanChannel {
		t.Errorf("type mismatch")
	}
	if decoded.State != ChanJoined {
		t.Errorf("state mismatch")
	}
}

func TestNetworkJSON(t *testing.T) {
	net := Network{
		UUID: "abc-123",
		Name: "Freenode",
		Nick: "testuser",
		Status: NetworkStatus{
			Connected: true,
			Secure:    true,
		},
	}

	data, err := json.Marshal(net)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded Network
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.UUID != net.UUID {
		t.Errorf("uuid mismatch")
	}
	if !decoded.Status.Connected {
		t.Errorf("expected connected=true")
	}
}

func TestEventStruct(t *testing.T) {
	ev := Event{
		Type: "msg",
		Data: []byte(`{"msg":{},"chan":1}`),
	}
	if ev.Type != "msg" {
		t.Errorf("expected type=msg")
	}
	if string(ev.Data) != `{"msg":{},"chan":1}` {
		t.Errorf("data mismatch")
	}
}

func TestSplit2(t *testing.T) {
	tests := []struct {
		input string
		sep   string
		want  []string
	}{
		{"a:b", ":", []string{"a", "b"}},
		{"a:b:c", ":", []string{"a", "b:c"}},
		{"abc", ":", nil},
		{"", ":", nil},
	}

	for _, tt := range tests {
		got := split2(tt.input, tt.sep)
		if tt.want == nil {
			if got != nil {
				t.Errorf("split2(%q,%q) = %v, want nil", tt.input, tt.sep, got)
			}
			continue
		}
		if len(got) != 2 || got[0] != tt.want[0] || got[1] != tt.want[1] {
			t.Errorf("split2(%q,%q) = %v, want %v", tt.input, tt.sep, got, tt.want)
		}
	}
}
