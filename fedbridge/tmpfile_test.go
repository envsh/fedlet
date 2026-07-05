package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandleTmpFile_MethodNotAllowed(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/tmpfile", nil)
	handleTmpFile(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("code: got %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleTmpFile_NoFile(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/tmpfile", nil)
	handleTmpFile(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleTmpFile_TmpfileLinkOK(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"downloadLink":"https://d.tmpfile.link/abc"}`))
	}))
	defer ok.Close()

	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fail.Close()

	_tmpfileLinkURL = ok.URL
	_tempfileOrgURL = fail.URL
	_storageToInitURL = fail.URL
	defer func() {
		_tmpfileLinkURL = "https://tmpfile.link/api/upload"
		_tempfileOrgURL = "https://tempfile.org/api/upload/local"
		_storageToInitURL = "https://storage.to/api/upload/init"
		_storageToConfURL = "https://storage.to/api/upload/confirm"
	}()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "test.txt")
	fw.Write([]byte("hello"))
	mw.Close()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/tmpfile", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	handleTmpFile(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code: got %d, want %d", w.Code, http.StatusOK)
	}
	var resp tmpfileResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.URL != "https://d.tmpfile.link/abc" {
		t.Errorf("url: got %q", resp.URL)
	}
	if resp.Service != "tmpfile.link" {
		t.Errorf("service: got %q", resp.Service)
	}
}

func TestHandleTmpFile_Fallback(t *testing.T) {
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fail.Close()

	var step int
	var stSrv *httptest.Server
	stSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step++
		switch step {
		case 1:
			w.Write([]byte(`{"success":true,"upload_url":"` + stSrv.URL + `/put","r2_key":"k1"}`))
		case 2:
			w.WriteHeader(http.StatusOK)
		case 3:
			w.Write([]byte(`{"success":true,"file":{"raw_url":"https://storage.to/r/xyz"}}`))
		}
	}))
	defer stSrv.Close()

	_tmpfileLinkURL = fail.URL
	_tempfileOrgURL = fail.URL
	_storageToInitURL = stSrv.URL
	_storageToConfURL = stSrv.URL
	defer func() {
		_tmpfileLinkURL = "https://tmpfile.link/api/upload"
		_tempfileOrgURL = "https://tempfile.org/api/upload/local"
		_storageToInitURL = "https://storage.to/api/upload/init"
		_storageToConfURL = "https://storage.to/api/upload/confirm"
	}()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "t.txt")
	fw.Write([]byte("data"))
	mw.Close()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/tmpfile", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	handleTmpFile(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code: got %d, want %d", w.Code, http.StatusOK)
	}
	var resp tmpfileResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Service != "storage.to" {
		t.Errorf("service: got %q, want storage.to", resp.Service)
	}
	if resp.URL != "https://storage.to/r/xyz" {
		t.Errorf("url: got %q, want https://storage.to/r/xyz", resp.URL)
	}
}

func TestHandleTmpFile_AllFailed(t *testing.T) {
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fail.Close()

	_tmpfileLinkURL = fail.URL
	_tempfileOrgURL = fail.URL
	_storageToInitURL = fail.URL
	defer func() {
		_tmpfileLinkURL = "https://tmpfile.link/api/upload"
		_tempfileOrgURL = "https://tempfile.org/api/upload/local"
		_storageToInitURL = "https://storage.to/api/upload/init"
		_storageToConfURL = "https://storage.to/api/upload/confirm"
	}()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "t.txt")
	fw.Write([]byte("data"))
	mw.Close()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/tmpfile", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	handleTmpFile(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("code: got %d, want %d", w.Code, http.StatusBadGateway)
	}
	var resp tmpfileResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error == "" {
		t.Error("expected error message")
	}
}

func TestUploadTmpfileLink_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"downloadLink":"https://d.tmpfile.link/file"}`))
	}))
	defer srv.Close()

	u, err := uploadTmpfileLinkWithURL(srv.URL, "test.txt", []byte("data"))
	if err != nil {
		t.Fatalf("uploadTmpfileLink: %v", err)
	}
	if u != "https://d.tmpfile.link/file" {
		t.Errorf("url: got %q", u)
	}
}

func TestUploadTmpfileLink_BadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := uploadTmpfileLinkWithURL(srv.URL, "t.txt", []byte("data"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUploadTmpfileLink_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	_, err := uploadTmpfileLinkWithURL(srv.URL, "t.txt", []byte("data"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUploadTempfileOrg_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"files":[{"id":"abc123"}]}`))
	}))
	defer srv.Close()

	u, err := uploadTempfileOrgWithURL(srv.URL, "t.txt", []byte("data"))
	if err != nil {
		t.Fatalf("uploadTempfileOrg: %v", err)
	}
	if u != "https://tempfile.org/abc123/download" {
		t.Errorf("url: got %q", u)
	}
}

func TestUploadTempfileOrg_BadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := uploadTempfileOrgWithURL(srv.URL, "t.txt", []byte("data"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUploadTempfileOrg_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":false}`))
	}))
	defer srv.Close()

	_, err := uploadTempfileOrgWithURL(srv.URL, "t.txt", []byte("data"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUploadStorageTo_Success(t *testing.T) {
	var step int
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step++
		switch step {
		case 1:
			w.Write([]byte(`{"success":true,"upload_url":"` + srv.URL + `/put","r2_key":"k1"}`))
		case 2:
			w.WriteHeader(http.StatusOK)
		case 3:
			w.Write([]byte(`{"success":true,"file":{"raw_url":"https://storage.to/r/abc"}}`))
		}
	}))
	defer srv.Close()

	_storageToInitURL = srv.URL
	_storageToConfURL = srv.URL
	defer func() {
		_storageToInitURL = "https://storage.to/api/upload/init"
		_storageToConfURL = "https://storage.to/api/upload/confirm"
	}()

	u, err := uploadStorageTo("t.txt", []byte("data"))
	if err != nil {
		t.Fatalf("uploadStorageTo: %v", err)
	}
	if u != "https://storage.to/r/abc" {
		t.Errorf("url: got %q", u)
	}
}

func TestUploadStorageTo_InitFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_storageToInitURL = srv.URL
	defer func() { _storageToInitURL = "https://storage.to/api/upload/init" }()

	_, err := uploadStorageTo("t.txt", []byte("data"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUploadStorageTo_PutFails(t *testing.T) {
	var step int
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step++
		if step == 1 {
			w.Write([]byte(`{"success":true,"upload_url":"` + srv.URL + `/put","r2_key":"k1"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_storageToInitURL = srv.URL
	defer func() { _storageToInitURL = "https://storage.to/api/upload/init" }()

	_, err := uploadStorageTo("t.txt", []byte("data"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNewTmpClient(t *testing.T) {
	c := newTmpClient()
	if c.Timeout != 600*time.Second {
		t.Errorf("timeout: got %v, want 600s", c.Timeout)
	}
}

func TestTmpfileJSON_Roundtrip(t *testing.T) {
	resp := tmpfileResponse{URL: "https://x/y", Service: "tmpfile.link"}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded tmpfileResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.URL != resp.URL {
		t.Errorf("url mismatch")
	}
	if decoded.Service != resp.Service {
		t.Errorf("service mismatch")
	}

	errResp := tmpfileResponse{Error: "something went wrong"}
	data, err = json.Marshal(errResp)
	if err != nil {
		t.Fatalf("marshal error response: %v", err)
	}
	var errDec tmpfileResponse
	if err := json.Unmarshal(data, &errDec); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if errDec.Error != errResp.Error {
		t.Errorf("error mismatch")
	}
}

func uploadTmpfileLinkWithURL(url, filename string, data []byte) (string, error) {
	defer func(u string) { _tmpfileLinkURL = u }(_tmpfileLinkURL)
	_tmpfileLinkURL = url
	return uploadTmpfileLink(filename, data)
}

func uploadTempfileOrgWithURL(url, filename string, data []byte) (string, error) {
	defer func(u string) { _tempfileOrgURL = u }(_tempfileOrgURL)
	_tempfileOrgURL = url
	return uploadTempfileOrg(filename, data)
}
