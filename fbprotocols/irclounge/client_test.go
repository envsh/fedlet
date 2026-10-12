package irclounge

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
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
	client, err := Connect(demoServer, "", "", 0)
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

// parseConfig 已移除。解析逻辑移至 fedbridge/irclounge.go，
// 使用 split2 + flag 分离 server/auth。

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

func TestLoungeUnifiedTypeMapping(t *testing.T) {
	tests := []struct {
		in   MessageType
		want string
	}{
		{MsgMessage, fbshared.MsgTypeCreate},
		{MsgNotice, fbshared.MsgTypeCreate},
		{MsgAction, "action"},
		{MsgJoin, fbshared.MsgTypeJoin},
		{MsgPart, fbshared.MsgTypeLeave},
		{MsgQuit, fbshared.MsgTypeLeave},
		{MsgKick, fbshared.MsgTypeLeave},
		{MsgTopic, fbshared.MsgTypeCreate},
	}
	for _, tt := range tests {
		if got := loungeUnifiedMsgType(tt.in); got != tt.want {
			t.Errorf("loungeUnifiedMsgType(%s) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestEmitLoungeDedup(t *testing.T) {
	seenMu.Lock()
	seenIDs = make(map[int]struct{})
	maxSeenID = 0
	seenMu.Unlock()

	if !markSeen(42) {
		t.Fatal("first markSeen(42) should be true")
	}
	if markSeen(42) {
		t.Fatal("second markSeen(42) should be false")
	}
	if !markSeen(43) {
		t.Fatal("markSeen(43) should be true")
	}
	if maxSeen() != 43 {
		t.Fatalf("maxSeen() = %d, want 43", maxSeen())
	}
}

func TestLoungeMessageToUnified(t *testing.T) {
	raw := []byte(`{"chan":5,"msg":{"id":1,"from":{"nick":"alice"},"text":"hi","type":"message","time":"2024-01-01T00:00:00Z"}}`)
	msg := Message{ID: 1, From: &UserInMessage{Nick: "alice"}, Text: "hi", Type: MsgMessage, Time: "2024-01-01T00:00:00Z"}
	um, ok := loungeMessageToUnified(msg, 5, raw)
	if !ok {
		t.Fatal("expected ok for non-empty message")
	}
	if um.ChatID != "5" || um.Username != "alice" || um.Text != "hi" || um.MsgType != fbshared.MsgTypeCreate {
		t.Fatalf("unexpected unified: %+v", um)
	}
	empty := Message{ID: 2, Type: MsgMessage}
	if _, ok := loungeMessageToUnified(empty, 5, nil); ok {
		t.Fatal("expected empty create message to be dropped")
	}
	j := Message{ID: 3, Type: MsgJoin}
	uj, ok := loungeMessageToUnified(j, 5, nil)
	if !ok || uj.MsgType != fbshared.MsgTypeJoin {
		t.Fatal("expected empty join to be published with join type")
	}
}

func TestParseMoreEvent(t *testing.T) {
	data := []byte(`{"chan":5,"messages":[{"id":10,"from":{"nick":"a"},"text":"x","type":"message"},{"id":11,"from":{"nick":"b"},"text":"y","type":"message"}],"totalMessages":50}`)
	var ev moreEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatalf("unmarshal more: %v", err)
	}
	if ev.Chan != 5 || len(ev.Messages) != 2 || ev.TotalMessages != 50 {
		t.Fatalf("unexpected moreEvent: %+v", ev)
	}
	if ev.Messages[0].ID != 10 || ev.Messages[1].ID != 11 {
		t.Fatalf("unexpected messages: %+v", ev.Messages)
	}
}

func TestInitMessagesParse(t *testing.T) {
	data := []byte(`{"active":5,"networks":[{"uuid":"u1","name":"Libera","nick":"me","channels":[{"id":5,"name":"#x","type":"channel","totalMessages":3,"messages":[{"id":1,"text":"a","type":"message"},{"id":2,"text":"b","type":"message"},{"id":3,"text":"c","type":"message"}]}]}]}`)
	var initData InitData
	if err := json.Unmarshal(data, &initData); err != nil {
		t.Fatalf("unmarshal init: %v", err)
	}
	if len(initData.Networks) != 1 || len(initData.Networks[0].Channels) != 1 {
		t.Fatalf("unexpected networks: %+v", initData)
	}
	ch := initData.Networks[0].Channels[0]
	if ch.TotalMessages != 3 || len(ch.Messages) != 3 {
		t.Fatalf("unexpected channel: %+v", ch)
	}
}

func TestSetPrimaryNetwork(t *testing.T) {
	lobbyMu.Lock()
	primaryLobbyID = 0
	netConnected = false
	lobbyMu.Unlock()

	net := Network{
		Channels: []Channel{
			{ID: 7, Name: "Libera", Type: ChanLobby},
			{ID: 8, Name: "#x", Type: ChanChannel},
		},
		Status: NetworkStatus{Connected: true},
	}
	setPrimaryNetwork(net)

	lobbyMu.Lock()
	gotLobby, gotConnected := primaryLobbyID, netConnected
	lobbyMu.Unlock()
	if gotLobby != 7 {
		t.Fatalf("primaryLobbyID = %d, want 7", gotLobby)
	}
	if !gotConnected {
		t.Fatalf("netConnected = false, want true")
	}
}

func TestFlushPendingJoinsGated(t *testing.T) {
	lobbyMu.Lock()
	primaryLobbyID = 0
	netConnected = false
	pendingJoins = []string{"#x"}
	lobbyMu.Unlock()

	flushPendingJoins(nil)

	lobbyMu.Lock()
	n := len(pendingJoins)
	lobbyMu.Unlock()
	if n != 1 {
		t.Fatalf("pendingJoins len = %d, want 1 (still queued while disconnected)", n)
	}
}
