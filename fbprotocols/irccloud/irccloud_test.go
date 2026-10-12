package irccloud

import (
	"encoding/json"
	"testing"
)

func TestMarkSeenDedup(t *testing.T) {
	st := &streamState{seenEIDs: make(map[int]map[uint64]struct{})}
	if !st.markSeen(3, 100) {
		t.Fatal("first mark should be new")
	}
	if st.markSeen(3, 100) {
		t.Fatal("second mark should be dup")
	}
	if !st.markSeen(4, 100) {
		t.Fatal("same eid on another bid should be new")
	}
	if !st.markSeen(0, 0) {
		t.Fatal("zero bid/eid should pass through")
	}
}

func TestIRCCloudMsgToUnifiedMapping(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		ok      bool
		msgType string
		chatID  string
		text    string
	}{
		{"buffer", `{"type":"buffer_msg","cid":2,"bid":5,"eid":1700000000000000,"chan":"#go","from":"alice","msg":"hi"}`, true, "buffer_msg", "2:#go", "hi"},
		{"me", `{"type":"buffer_me_msg","cid":2,"bid":5,"eid":1700000000000001,"chan":"#go","from":"bob","msg":"waves"}`, true, "action", "2:#go", "waves"},
		{"self", `{"type":"buffer_msg","cid":2,"bid":5,"eid":1700000000000002,"chan":"#go","from":"me","msg":"mine","self":true}`, false, "", "", ""},
		{"join", `{"type":"joined_channel","cid":2,"bid":5,"eid":1700000000000003,"chan":"#go","nick":"carol"}`, true, "join", "2:#go", ""},
		{"part", `{"type":"parted_channel","cid":2,"bid":5,"eid":1700000000000004,"chan":"#go","nick":"dave"}`, true, "leave", "2:#go", ""},
		{"quit", `{"type":"quit","cid":2,"bid":5,"eid":1700000000000005,"nick":"erin"}`, true, "leave", "", ""},
		{"other", `{"type":"idle"}`, false, "", "", ""},
	}
	for _, c := range cases {
		um, ok := irccloudMsgToUnified([]byte(c.raw))
		if ok != c.ok {
			t.Fatalf("%s: ok=%v want %v", c.name, ok, c.ok)
		}
		if !ok {
			continue
		}
		if um.MsgType != c.msgType {
			t.Errorf("%s: msgtype=%q want %q", c.name, um.MsgType, c.msgType)
		}
		if um.ChatID != c.chatID {
			t.Errorf("%s: chatid=%q want %q", c.name, um.ChatID, c.chatID)
		}
		if c.text != "" && um.Text != c.text {
			t.Errorf("%s: text=%q want %q", c.name, um.Text, c.text)
		}
	}
}

func TestParseAppConfig(t *testing.T) {
	cfg := parseAppConfig(`{"email":"a@b.com","password":"pw","channels":["#x","#y"]}`)
	if cfg.Email != "a@b.com" || cfg.Password != "pw" || len(cfg.Channels) != 2 {
		t.Fatalf("json config parse: %+v", cfg)
	}
	cfg = parseAppConfig("a@b.com:pw,#x,#y")
	if cfg.Email != "a@b.com" || cfg.Password != "pw" || len(cfg.Channels) != 2 {
		t.Fatalf("colon config parse: %+v", cfg)
	}
}

func TestSetShardDeriveHost(t *testing.T) {
	var sh struct {
		APIHost       string `json:"api_host"`
		WebsocketHost string `json:"websocket_host"`
		Cookie        string `json:"cookie"`
	}
	raw := `{"message":"set_shard","websocket_host":"api-6.irccloud.com","websocket_path":"/websocket/6","cookie":"abc"}`
	if err := json.Unmarshal([]byte(raw), &sh); err != nil {
		t.Fatal(err)
	}
	host := sh.APIHost
	if host == "" && sh.WebsocketHost != "" {
		host = "https://" + sh.WebsocketHost
	}
	if host != "https://api-6.irccloud.com" {
		t.Fatalf("derived host=%q", host)
	}
}
