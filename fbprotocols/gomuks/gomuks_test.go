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

func TestParseGomuksEventAttachments(t *testing.T) {
	raw := []byte(`{"event_id":"$e1","sender":"@a:s","type":"m.room.message","timestamp":1700000000000,"content":{"body":"pic.jpg","msgtype":"m.image","url":"mxc://s/id","info":{"mimetype":"image/jpeg","size":1234,"w":10,"h":20}}}`)
	um := parseGomuksEvent(raw, "!r:s", "room", "mxc://s/icon")
	if um == nil {
		t.Fatal("nil um")
	}
	if um.MsgType != "m.image" {
		t.Fatalf("msgtype=%q", um.MsgType)
	}
	if len(um.Attachments) != 1 {
		t.Fatalf("attachments=%v", um.Attachments)
	}
	a := um.Attachments[0]
	if a.AttachType != fbshared.AttachImage || a.URL != "mxc://s/id" ||
		a.MimeType != "image/jpeg" || a.Size != 1234 || a.Width != 10 ||
		a.Height != 20 || a.Filename != "pic.jpg" {
		t.Fatalf("bad attachment: %+v", a)
	}
}

func TestParseGomuksEventSticker(t *testing.T) {
	raw := []byte(`{"event_id":"$s1","sender":"@a:s","type":"m.sticker","timestamp":1700000000000,"content":{"body":"st.webp","url":"mxc://s/st","info":{"mimetype":"image/webp","size":9,"w":3,"h":4}}}`)
	um := parseGomuksEvent(raw, "!r:s", "", "")
	if um == nil {
		t.Fatal("nil um")
	}
	if um.MsgType != "m.sticker" || len(um.Attachments) != 1 ||
		um.Attachments[0].AttachType != fbshared.AttachSticker {
		t.Fatalf("bad sticker: msgtype=%q attachments=%v", um.MsgType, um.Attachments)
	}
}
