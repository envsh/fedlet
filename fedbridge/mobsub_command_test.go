package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestMaskSecret(t *testing.T) {
	cases := map[string]string{
		"":             "***",
		"abcdef":       "***",
		"abcdefg":      "abc***efg",
		"AAAABBBBCCCC": "AAA***CCC",
	}
	for in, want := range cases {
		if got := maskSecret(in); got != want {
			t.Errorf("maskSecret(%q) = %q, want %q", in, got, want)
		}
	}
}
