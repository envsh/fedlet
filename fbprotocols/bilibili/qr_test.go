package bilibili

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestKeyFromScanURL(t *testing.T) {
	u := "https://account.bilibili.com/h5/account-h5/auth/scan-web?navhide=1&callback=close&qrcode_key=abc123&from="
	if got := keyFromScanURL(u); got != "abc123" {
		t.Fatalf("keyFromScanURL: got %q want abc123", got)
	}
	if got := keyFromScanURL("https://example.com/no-key"); got != "" {
		t.Fatalf("keyFromScanURL no-key: got %q want empty", got)
	}
	if got := keyFromScanURL("://bad"); got != "" {
		t.Fatalf("keyFromScanURL bad-url: got %q want empty", got)
	}
}

func TestQRScanStatePriority(t *testing.T) {
	if sc := qrScanState(86101, 0); sc != 86101 {
		t.Fatalf("state from data.code: got %d want 86101", sc)
	}
	if sc := qrScanState(0, 86038); sc != 86038 {
		t.Fatalf("state fallback to envelope: got %d want 86038", sc)
	}
	if sc := qrScanState(0, 0); sc != 0 {
		t.Fatalf("state both zero: got %d want 0", sc)
	}
}

func TestParseQRGenFull(t *testing.T) {
	g, err := parseQRGen(json.RawMessage(`{"url":"https://account.bilibili.com/h5/account-h5/auth/scan-web?navhide=1&callback=close&qrcode_key=e901610c22ddda35257eb95615dc3c9b&from=","qrcode_key":"e901610c22ddda35257eb95615dc3c9b"}`))
	if err != nil {
		t.Fatalf("parseQRGen: %v", err)
	}
	if g.QRCodeKey != "e901610c22ddda35257eb95615dc3c9b" {
		t.Fatalf("qrcode_key: got %q", g.QRCodeKey)
	}
	if !strings.Contains(g.URL, "qrcode_key=e901610c22ddda35257eb95615dc3c9b") {
		t.Fatalf("url: got %q", g.URL)
	}
}

func TestParseQRGenKeyFromURL(t *testing.T) {
	g, err := parseQRGen(json.RawMessage(`{"url":"https://account.bilibili.com/h5/account-h5/auth/scan-web?qrcode_key=abc123&from="}`))
	if err != nil {
		t.Fatalf("parseQRGen: %v", err)
	}
	if g.QRCodeKey != "abc123" {
		t.Fatalf("qrcode_key from url: got %q want abc123", g.QRCodeKey)
	}
}

func TestParseQRGenEmpty(t *testing.T) {
	g, err := parseQRGen(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("parseQRGen: %v", err)
	}
	if g.URL != "" || g.QRCodeKey != "" {
		t.Fatalf("expected empty parse, got %+v", g)
	}
}

func TestLoginStateFinalized(t *testing.T) {
	s := &loginState{stage: stageIdle}
	if s.finalized() {
		t.Fatalf("idle stage reported finalized")
	}
	s.set(stageWaiting, "")
	if s.finalized() {
		t.Fatalf("waiting stage reported finalized")
	}
	s.set(stageFailed, "boom")
	if s.finalized() {
		t.Fatalf("failed stage reported finalized")
	}
	s.set(stageDone, "")
	if !s.finalized() {
		t.Fatalf("done stage not reported finalized")
	}
}
