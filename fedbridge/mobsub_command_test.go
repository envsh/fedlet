package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"encoding/json"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func TestNtfyTopicRe(t *testing.T) {
	for _, s := range []string{"abc", "a-b_c", "ABC123", strings.Repeat("a", 64)} {
		if !ntfyTopicRe.MatchString(s) {
			t.Errorf("want valid: %q", s)
		}
	}
	for _, s := range []string{"", "a.b", "a/b", "a b", strings.Repeat("a", 65)} {
		if ntfyTopicRe.MatchString(s) {
			t.Errorf("want invalid: %q", s)
		}
	}
}

func TestBuildMobSubConfig(t *testing.T) {
	cfg := buildMobSubConfig("https://ntfy.sh", "11111111-2222-3333-4444-555555555555", "https://updates.push.services.mozilla.com/wpush/v1/xxxx")
	if cfg.uaid != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("uaid = %q", cfg.uaid)
	}
	if cfg.ntfyBase != "https://ntfy.sh" {
		t.Fatalf("ntfyBase = %q", cfg.ntfyBase)
	}
	if cfg.endpoint == "" {
		t.Fatalf("endpoint empty")
	}
	if cfg.server != mobsubDefaultMozServer {
		t.Fatalf("server = %q, want default", cfg.server)
	}
}

func TestMobsubWSServer(t *testing.T) {
	cases := []struct{ endpoint, want string }{
		{"", mobsubDefaultMozServer},
		{"://bad", mobsubDefaultMozServer},
		{"not a url", mobsubDefaultMozServer},
		{"https://updates.push.services.mozilla.com/wpush/v1/xxxx", "wss://push.services.mozilla.com/"},
		{"https://updates-autopush.dev.mozaws.net/wpush/v1/xxxx", "wss://autoconnect.dev.mozaws.net/"},
		{"https://updates-autopush.stage.mozaws.net/wpush/v1/xxxx", "wss://autoconnect.stage.mozaws.net/"},
		{"https://update.push.example.com/wpush/v1/xxxx", "wss://update.push.example.com/"},
		{"http://localhost:8082/wpush/v1/xxxx", "ws://localhost:8082/"},
	}
	for _, c := range cases {
		if got := mobsubWSServer(c.endpoint); got != c.want {
			t.Errorf("mobsubWSServer(%q) = %q, want %q", c.endpoint, got, c.want)
		}
	}
}

func TestMobsubSameUAID(t *testing.T) {
	pairs := [][2]string{
		{"5619557c-86fe-4711-8078-d1fd6987aef7", "5619557c86fe47118078d1fd6987aef7"},
		{"5619557C-86FE-4711-8078-D1FD6987AEF7", "5619557c-86fe-4711-8078-d1fd6987aef7"},
	}
	for _, p := range pairs {
		if !mobsubSameUAID(p[0], p[1]) {
			t.Errorf("mobsubSameUAID(%q,%q) = false, want true", p[0], p[1])
		}
	}
	if mobsubSameUAID("11111111-2222-3333-4444-555555555555", "99999999-2222-3333-4444-555555555555") {
		t.Error("different uaids matched")
	}
}

func TestDecryptMozillaWebPush(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	sub := &webpush.Subscription{
		Endpoint: srv.URL,
		Keys: webpush.Keys{
			P256dh: base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
			Auth:   base64.RawURLEncoding.EncodeToString(auth),
		},
	}
	vpriv, vpub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	opts := &webpush.Options{
		Subscriber:      "test@example.com",
		VAPIDPublicKey:  vpub,
		VAPIDPrivateKey: vpriv,
		TTL:             30,
	}
	want := []byte(`{"hello":"mobsub"}`)
	if _, err := webpush.SendNotification(want, sub, opts); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("no payload received by endpoint")
	}

	st := &mobsubState{
		ECPrivate:  base64.RawURLEncoding.EncodeToString(priv.Bytes()),
		ECPublic:   base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		AuthSecret: base64.RawURLEncoding.EncodeToString(auth),
	}
	pt, err := decryptMozillaWebPush(base64.RawURLEncoding.EncodeToString(got), nil, st)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(pt) != string(want) {
		t.Fatalf("plaintext = %q, want %q", pt, want)
	}
}

func TestMobsubSelfSend(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	st := &mobsubState{
		UAID:       "11111111-2222-3333-4444-555555555555",
		ECPrivate:  base64.RawURLEncoding.EncodeToString(priv.Bytes()),
		ECPublic:   base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		AuthSecret: base64.RawURLEncoding.EncodeToString(auth),
	}
	vpriv, vpub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	sub := &webpush.Subscription{
		Endpoint: srv.URL,
		Keys:     webpush.Keys{P256dh: st.ECPublic, Auth: st.AuthSecret},
	}
	opts := &webpush.Options{
		Subscriber:      "mobsub@localhost",
		VAPIDPublicKey:  vpub,
		VAPIDPrivateKey: vpriv,
		TTL:             39,
	}
	want := []byte(`{"self":"mobsub"}`)
	if err := sendMobSubSelf(sub, opts, want); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("no payload received by endpoint")
	}
	pt, err := decryptMozillaWebPush(base64.RawURLEncoding.EncodeToString(got), nil, st)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(pt) != string(want) {
		t.Fatalf("plaintext = %q, want %q", pt, want)
	}
}

func TestDeriveMobSubState(t *testing.T) {
	const uaid = "11111111-2222-3333-4444-555555555555"
	a, err := deriveMobSubState(uaid)
	if err != nil {
		t.Fatal(err)
	}
	b, err := deriveMobSubState(uaid)
	if err != nil {
		t.Fatal(err)
	}
	if a.ECPrivate != b.ECPrivate || a.ECPublic != b.ECPublic || a.AuthSecret != b.AuthSecret {
		t.Fatalf("not deterministic:\n a=%+v\n b=%+v", a, b)
	}
	c, err := deriveMobSubState("99999999-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	if a.ECPrivate == c.ECPrivate {
		t.Fatal("different uaid produced same key")
	}

	// 外部发送方用派生公钥加密 → 用同一 state 解密(往返)。
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	vpriv, vpub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	sub := &webpush.Subscription{
		Endpoint: srv.URL,
		Keys:     webpush.Keys{P256dh: a.ECPublic, Auth: a.AuthSecret},
	}
	opts := &webpush.Options{
		Subscriber:      "sender@example.com",
		VAPIDPublicKey:  vpub,
		VAPIDPrivateKey: vpriv,
		TTL:             39,
	}
	want := []byte(`{"ext":"push"}`)
	if _, err := webpush.SendNotification(want, sub, opts); err != nil {
		t.Fatal(err)
	}
	pt, err := decryptMozillaWebPush(base64.RawURLEncoding.EncodeToString(got), nil, a)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(pt) != string(want) {
		t.Fatalf("plaintext = %q, want %q", pt, want)
	}
}

func TestToDisplaySecret(t *testing.T) {
	cases := map[string]string{
		"":             "***",
		"abcdef":       "***",
		"abcdefg":      "abc***efg",
		"AAAABBBBCCCC": "AAA***CCC",
	}
	for in, want := range cases {
		if got := toDisplaySecret(in); got != want {
			t.Errorf("toDisplaySecret(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsSendMsg(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"完整", `{"type":"imap_mail","id":"a","message":"hi"}`, true},
		{"缺 message", `{"type":"imap_mail","id":"a"}`, false},
		{"缺 id", `{"type":"x","message":"hi"}`, false},
		{"缺 type", `{"id":"a","message":"hi"}`, false},
		{"file 替代 message", `{"type":"x","id":"a","file":"f.png"}`, true},
		{"数字 message", `{"type":"x","id":"a","message":1}`, true},
		{"非 JSON", `not json`, false},
		{"空对象", `{}`, false},
		{"原始 mobsub 无 type", `{"proto_type":"mobsub","seq":3}`, false},
	}
	for _, c := range cases {
		if got := isSendMsg([]byte(c.raw)); got != c.want {
			t.Errorf("%s: isSendMsg=%v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseMobSubMsg(t *testing.T) {
	raw := `{"type":"imap_mail","id":"a","message":"hi","relates_to":"$1,$2","mentions":"@u"}`
	if !isSendMsg([]byte(raw)) {
		t.Fatal("want sendmsg")
	}
	m := parseMobSubMsg([]byte(raw))
	if m.Type != "imap_mail" || m.ID != "a" || m.Message != "hi" {
		t.Fatalf("parsed = %+v", m)
	}
	if m.RelatesTo != "$1,$2" || m.Mentions != "@u" {
		t.Fatalf("relates_to=%q mentions=%q", m.RelatesTo, m.Mentions)
	}
}

func TestMobsubBasicMarshal(t *testing.T) {
	b, err := json.Marshal(mobsubBasic{
		ProtoType: "mobsub", UAID: "", VirtIP4: "10.0.0.1",
		PeerID7: "abcdefg", Seq: 3, TS: 1789357931,
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["proto_type"] != "mobsub" || m["virtip4"] != "10.0.0.1" ||
		m["peerid7"] != "abcdefg" || m["uaid"] != "" {
		t.Fatalf("marshal = %s", b)
	}
	if m["seq"] != float64(3) || m["ts"] != float64(1789357931) {
		t.Fatalf("seq/ts = %v/%v", m["seq"], m["ts"])
	}
}
