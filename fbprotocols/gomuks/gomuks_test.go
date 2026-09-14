package gomuks

import (
	"testing"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

func TestGomuksSendDataBasics(t *testing.T) {
	d := gomuksSendData("!room:server", "hello", nil, nil)
	if d["room_id"] != "!room:server" || d["text"] != "hello" {
		t.Fatalf("bad base: %v", d)
	}
	if _, ok := d["relates_to"]; ok {
		t.Fatalf("unexpected relates_to: %v", d)
	}
	if _, ok := d["mentions"]; ok {
		t.Fatalf("unexpected mentions: %v", d)
	}
}

func TestGomuksSendDataBaseContent(t *testing.T) {
	d := gomuksSendData("!r", "pic", map[string]any{"msgtype": "m.image", "url": "mxc://a/b"}, nil)
	if _, ok := d["base_content"]; !ok {
		t.Fatalf("missing base_content: %v", d)
	}
}

func TestGomuksSendDataExtra(t *testing.T) {
	extra := &fbshared.SendExtra{
		RelatesTo: []string{"$evt1", "$evt2"},
		Mentions:  []string{"@u1:s", "@u2:s"},
	}
	d := gomuksSendData("!r", "hi", nil, extra)

	mt, ok := d["mentions"].(map[string]any)
	if !ok {
		t.Fatalf("mentions not map: %v", d["mentions"])
	}
	if uids, ok := mt["user_ids"].([]string); !ok || len(uids) != 2 {
		t.Fatalf("bad mentions user_ids: %v", mt["user_ids"])
	}

	rt, ok := d["relates_to"].(map[string]any)
	if !ok {
		t.Fatalf("relates_to not map: %v", d["relates_to"])
	}
	inr, ok := rt["m.in_reply_to"].(map[string]any)
	if !ok || inr["event_id"] != "$evt1" {
		t.Fatalf("bad in_reply_to: %v", rt)
	}
}

func TestGomuksSendDataExtraEmpty(t *testing.T) {
	d := gomuksSendData("!r", "hi", nil, &fbshared.SendExtra{})
	if _, ok := d["relates_to"]; ok {
		t.Fatalf("unexpected relates_to: %v", d)
	}
	if _, ok := d["mentions"]; ok {
		t.Fatalf("unexpected mentions: %v", d)
	}
}
