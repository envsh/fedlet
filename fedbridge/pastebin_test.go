package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandlePastebin_MethodNotAllowed(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/pastebin", nil)
	handlePastebin(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("code: got %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandlePastebin_InvalidJSON(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/pastebin",
		strings.NewReader(`not json`))
	handlePastebin(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandlePastebin_EmptyText(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/pastebin",
		strings.NewReader(`{"text":""}`))
	handlePastebin(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code: got %d, want %d", w.Code, http.StatusBadRequest)
	}
	var resp pastebinResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error == "" {
		t.Error("expected error message")
	}
}

func TestHandlePastebin_PasteRsOK(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("https://paste.rs/abc"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_pasteRsURL = srv.URL + "/"
	defer func() { _pasteRsURL = "https://paste.rs/" }()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/pastebin",
		strings.NewReader(`{"text":"hello"}`))
	handlePastebin(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code: got %d, want %d", w.Code, http.StatusOK)
	}
	var resp pastebinResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.URL != "https://paste.rs/abc" {
		t.Errorf("url: got %q", resp.URL)
	}
	if resp.Service != "paste.rs" {
		t.Errorf("service: got %q", resp.Service)
	}
}

func TestHandlePastebin_Fallback(t *testing.T) {
	pasteFail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer pasteFail.Close()

	dpasteOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("https://dpaste.com/xyz"))
	}))
	defer dpasteOK.Close()

	_pasteRsURL = pasteFail.URL + "/"
	_dpasteComURL = dpasteOK.URL + "/"
	defer func() {
		_pasteRsURL = "https://paste.rs/"
		_dpasteComURL = "https://dpaste.com/api/v2/"
	}()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/pastebin",
		strings.NewReader(`{"text":"hello"}`))
	handlePastebin(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code: got %d, want %d", w.Code, http.StatusOK)
	}
	var resp pastebinResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Service != "dpaste.com" {
		t.Errorf("service: got %q, want dpaste.com", resp.Service)
	}
}

func TestHandlePastebin_AllFailed(t *testing.T) {
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fail.Close()

	_pasteRsURL = fail.URL + "/"
	_dpasteComURL = fail.URL + "/"
	defer func() {
		_pasteRsURL = "https://paste.rs/"
		_dpasteComURL = "https://dpaste.com/api/v2/"
	}()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/pastebin",
		strings.NewReader(`{"text":"hello"}`))
	handlePastebin(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("code: got %d, want %d", w.Code, http.StatusBadGateway)
	}
	var resp pastebinResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error == "" {
		t.Error("expected error message")
	}
}

func TestUploadPasteRs_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("https://paste.rs/abc123"))
	}))
	defer srv.Close()

	u, err := uploadPasteRsWithURL(srv.URL+"/", "hello")
	if err != nil {
		t.Fatalf("uploadPasteRs: %v", err)
	}
	if u != "https://paste.rs/abc123" {
		t.Errorf("url: got %q", u)
	}
}

func TestUploadPasteRs_BadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := uploadPasteRsWithURL(srv.URL+"/", "hello")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUploadPasteRs_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte{})
	}))
	defer srv.Close()

	_, err := uploadPasteRsWithURL(srv.URL+"/", "hello")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUploadPasteRs_NonURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	_, err := uploadPasteRsWithURL(srv.URL+"/", "hello")
	if err == nil {
		t.Fatal("expected error for non-URL response")
	}
}

func TestUploadDpasteCom_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("https://dpaste.com/xyz789"))
	}))
	defer srv.Close()

	u, err := uploadDpasteComWithURL(srv.URL+"/", "hello")
	if err != nil {
		t.Fatalf("uploadDpasteCom: %v", err)
	}
	if u != "https://dpaste.com/xyz789" {
		t.Errorf("url: got %q", u)
	}
}

func TestUploadDpasteCom_BadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := uploadDpasteComWithURL(srv.URL+"/", "hello")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUploadDpasteCom_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte{})
	}))
	defer srv.Close()

	_, err := uploadDpasteComWithURL(srv.URL+"/", "hello")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNewClient(t *testing.T) {
	c := newClient()
	if c.Timeout != 15*time.Second {
		t.Errorf("timeout: got %v, want 15s", c.Timeout)
	}
}

func TestPastebinJSON_Roundtrip(t *testing.T) {
	req := pastebinRequest{Text: "hello world"}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded pastebinRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Text != req.Text {
		t.Errorf("text: got %q, want %q", decoded.Text, req.Text)
	}

	resp := pastebinResponse{URL: "https://x/y", Service: "test"}
	data, err = json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var respDec pastebinResponse
	if err := json.Unmarshal(data, &respDec); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if respDec.URL != resp.URL {
		t.Errorf("url mismatch")
	}
}

func uploadPasteRsWithURL(url, text string) (string, error) {
	defer func(u string) { _pasteRsURL = u }(_pasteRsURL)
	_pasteRsURL = url
	return uploadPasteRs(text)
}

func uploadDpasteComWithURL(url, text string) (string, error) {
	defer func(u string) { _dpasteComURL = u }(_dpasteComURL)
	_dpasteComURL = url
	return uploadDpasteCom(text)
}
