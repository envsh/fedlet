package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

func postSend(t *testing.T, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/messages/send", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handleMessageSend(rr, req)
	return rr
}

// C1: 本机未注册的 ctype(且无 peer) 必须返回 400 + code=unknown_ctype，
// 而不是被压成 500。ttl=1(来自 peer 转发)与 ttl=""(本地原始)都应如此。
func TestHandleMessageSendUnknownCtype(t *testing.T) {
	for _, ttl := range []string{"", "1"} {
		form := url.Values{"type": {"mtxlite_room"}, "id": {"!room:example.com"}, "message": {"hi"}}
		if ttl != "" {
			form.Set("ttl", ttl)
		}
		rr := postSend(t, form)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("ttl=%q status: got %d want 400; body=%s", ttl, rr.Code, rr.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("ttl=%q decode: %v body=%s", ttl, err, rr.Body.String())
		}
		if body["code"] != "unknown_ctype" {
			t.Fatalf("ttl=%q code: got %q want unknown_ctype; body=%s", ttl, body["code"], rr.Body.String())
		}
	}
}

// 已注册的 ctype 不应触发 C1 早退：仍进入 DispatchSend → SendFn。
func TestHandleMessageSendKnownCtypeDispatches(t *testing.T) {
	const ct = "test_sink_ctype"
	sent := false
	RegisterProtocol(&ProtocolInfo{
		Name:       ct,
		Ctypes:     []string{ct},
		Capacities: ProtocolCapacities{CanSend: true},
		SendFn: func(to, msg, msgType string, filedata []byte, fileinfo *fbshared.MediaDataInfo, extra *fbshared.SendExtra) (fbshared.SendResult, error) {
			sent = true
			return fbshared.SendResult{MsgID: "m1"}, nil
		},
	})
	defer delete(ctypeRegistry, ct)

	rr := postSend(t, url.Values{"type": {ct}, "id": {"!room:example.com"}, "message": {"hi"}})
	if !sent {
		t.Fatalf("SendFn not called; status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Code == http.StatusBadRequest {
		t.Fatalf("known ctype should not hit C1 early-return; body=%s", rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if body["proto_msgid"] != "m1" {
		t.Fatalf("proto_msgid: got %v want m1; body=%s", body["proto_msgid"], rr.Body.String())
	}
}
