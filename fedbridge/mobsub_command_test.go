package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
)

var canonicalUUIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestMobsubNameToChannelID(t *testing.T) {
	a := mobsubNameToChannelID("mobsub.example.com")
	if b := mobsubNameToChannelID("mobsub.example.com"); a != b {
		t.Fatalf("not deterministic: %q != %q", a, b)
	}
	if !canonicalUUIDRe.MatchString(a) {
		t.Fatalf("not a canonical uuid: %q", a)
	}
	if c := mobsubNameToChannelID("other-name"); c == a {
		t.Fatalf("collision across names: %q", c)
	}
}

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
		Subscriber:      "mailto:test@example.com",
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
