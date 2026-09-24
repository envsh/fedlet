package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

func init() {
	http.HandleFunc("/api/tmpfile", handleTmpFile)
}

var (
	_tmpfileLinkURL   = "https://tmpfile.link/api/upload"
	_tempfileOrgURL   = "https://tempfile.org/api/upload/local"
	_storageToInitURL = "https://storage.to/api/upload/init"
	_storageToConfURL = "https://storage.to/api/upload/confirm"
	_catboxURL        = "https://catbox.moe/user/api.php"
	_litterboxURL     = "https://litterbox.catbox.moe/resources/internals/api.php"
	_mhimgURL         = "https://mhimg.cn/api/v1/upload"
	_scdnIoURL        = "https://img.scdn.io/api/v1.php"
)

type tmpfileResponse struct {
	URL     string `json:"url,omitempty"`
	Service string `json:"service,omitempty"`
	Error   string `json:"error,omitempty"`
}

func newTmpClient() *http.Client {
	return &http.Client{Timeout: 600 * time.Second}
}

func handleTmpFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 100<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, fmt.Sprintf("failed to parse upload: %s", err), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, "file field 'file' is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		writeErr(w, fmt.Sprintf("failed to read file: %s", err), http.StatusInternalServerError)
		return
	}

	uploaders := []struct {
		name string
		fn   func(string, []byte) (string, error)
	}{
		{"tmpfile.link", uploadTmpfileLink},
		{"tempfile.org", uploadTempfileOrg},
		{"storage.to", uploadStorageTo},
		{"catbox", uploadCatbox},
		{"litterbox", uploadLitterbox},
		{"mhimg.cn", uploadMhimg},
		{"img.scdn.io", uploadScdnIo},
	}

	perm := rand.Perm(len(uploaders))
	var firstErr error
	for _, i := range perm {
		url, err := uploaders[i].fn(header.Filename, data)
		if err == nil {
			log.Printf("tmpfile: uploaded via %s -> %s", uploaders[i].name, url)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(tmpfileResponse{URL: url, Service: uploaders[i].name})
			return
		}
		log.Printf("tmpfile: %s failed: %s", uploaders[i].name, err)
		if firstErr == nil {
			firstErr = err
		}
	}
	writeErr(w, fmt.Sprintf("all tmpfile services failed: %s", firstErr), http.StatusBadGateway)
}

func uploadTmpfileLink(filename string, data []byte) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("tmpfile.link: create form: %w", err)
	}
	if _, err := fw.Write(data); err != nil {
		return "", fmt.Errorf("tmpfile.link: write data: %w", err)
	}
	w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 590*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, _tmpfileLinkURL, &buf)
	if err != nil {
		return "", fmt.Errorf("tmpfile.link: create request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Close = true

	resp, err := newTmpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("tmpfile.link: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("tmpfile.link: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tmpfile.link: bad status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		DownloadLink string `json:"downloadLink"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("tmpfile.link: parse json: %w", err)
	}
	if result.DownloadLink == "" {
		return "", fmt.Errorf("tmpfile.link: empty downloadLink")
	}
	return result.DownloadLink, nil
}

func uploadTempfileOrg(filename string, data []byte) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("files", filename)
	if err != nil {
		return "", fmt.Errorf("tempfile.org: create form: %w", err)
	}
	if _, err := fw.Write(data); err != nil {
		return "", fmt.Errorf("tempfile.org: write data: %w", err)
	}
	w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 590*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, _tempfileOrgURL, &buf)
	if err != nil {
		return "", fmt.Errorf("tempfile.org: create request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Close = true

	resp, err := newTmpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("tempfile.org: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("tempfile.org: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tempfile.org: bad status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Success bool `json:"success"`
		Files   []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("tempfile.org: parse json: %w", err)
	}
	if !result.Success || len(result.Files) == 0 || result.Files[0].ID == "" {
		return "", fmt.Errorf("tempfile.org: unexpected response: %s", strings.TrimSpace(string(body)))
	}
	return fmt.Sprintf("https://tempfile.org/%s/download", result.Files[0].ID), nil
}

func uploadStorageTo(filename string, data []byte) (string, error) {
	client := newTmpClient()
	contentType := "application/octet-stream"

	// Step 1: Init — get presigned upload URL
	initPayload := fmt.Sprintf(`{"filename":"%s","content_type":"%s","size":%d}`,
		filename, contentType, len(data))

	ctx1, cancel1 := context.WithTimeout(context.Background(), 590*time.Second)
	defer cancel1()

	req1, err := http.NewRequestWithContext(ctx1, http.MethodPost, _storageToInitURL, strings.NewReader(initPayload))
	if err != nil {
		return "", fmt.Errorf("storage.to: init request: %w", err)
	}
	req1.Header.Set("Content-Type", "application/json")
	req1.Close = true

	resp1, err := client.Do(req1)
	if err != nil {
		return "", fmt.Errorf("storage.to: init: %w", err)
	}
	defer resp1.Body.Close()

	var initResult struct {
		Success   bool   `json:"success"`
		UploadURL string `json:"upload_url"`
		R2Key     string `json:"r2_key"`
	}
	if err := json.NewDecoder(resp1.Body).Decode(&initResult); err != nil {
		return "", fmt.Errorf("storage.to: init parse: %w", err)
	}
	if !initResult.Success || initResult.UploadURL == "" || initResult.R2Key == "" {
		return "", fmt.Errorf("storage.to: init failed: %+v", initResult)
	}

	// Step 2: PUT file data to presigned URL
	ctx2, cancel2 := context.WithTimeout(context.Background(), 590*time.Second)
	defer cancel2()

	req2, err := http.NewRequestWithContext(ctx2, http.MethodPut, initResult.UploadURL, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("storage.to: put request: %w", err)
	}
	req2.Header.Set("Content-Type", contentType)
	req2.Close = true

	resp2, err := client.Do(req2)
	if err != nil {
		return "", fmt.Errorf("storage.to: put: %w", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		return "", fmt.Errorf("storage.to: put bad status %d", resp2.StatusCode)
	}

	// Step 3: Confirm — get download URL
	confPayload := fmt.Sprintf(`{"r2_key":"%s","filename":"%s","content_type":"%s","size":%d}`,
		initResult.R2Key, filename, contentType, len(data))

	ctx3, cancel3 := context.WithTimeout(context.Background(), 590*time.Second)
	defer cancel3()

	req3, err := http.NewRequestWithContext(ctx3, http.MethodPost, _storageToConfURL, strings.NewReader(confPayload))
	if err != nil {
		return "", fmt.Errorf("storage.to: confirm request: %w", err)
	}
	req3.Header.Set("Content-Type", "application/json")
	req3.Close = true

	resp3, err := client.Do(req3)
	if err != nil {
		return "", fmt.Errorf("storage.to: confirm: %w", err)
	}
	defer resp3.Body.Close()

	var confResult struct {
		Success bool `json:"success"`
		File    struct {
			RawURL string `json:"raw_url"`
		} `json:"file"`
	}
	if err := json.NewDecoder(resp3.Body).Decode(&confResult); err != nil {
		return "", fmt.Errorf("storage.to: confirm parse: %w", err)
	}
	if !confResult.Success || confResult.File.RawURL == "" {
		return "", fmt.Errorf("storage.to: confirm failed: %+v", confResult)
	}
	return confResult.File.RawURL, nil
}

func uploadCatbox(filename string, data []byte) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("reqtype", "fileupload"); err != nil {
		return "", fmt.Errorf("catbox: write field: %w", err)
	}
	fw, err := w.CreateFormFile("fileToUpload", filename)
	if err != nil {
		return "", fmt.Errorf("catbox: create form: %w", err)
	}
	if _, err := fw.Write(data); err != nil {
		return "", fmt.Errorf("catbox: write data: %w", err)
	}
	w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 590*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, _catboxURL, &buf)
	if err != nil {
		return "", fmt.Errorf("catbox: create request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Close = true

	resp, err := newTmpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("catbox: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("catbox: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("catbox: bad status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	url := strings.TrimSpace(string(body))
	if !strings.HasPrefix(url, "http") {
		return "", fmt.Errorf("catbox: unexpected response: %s", url)
	}
	return url, nil
}

func uploadLitterbox(filename string, data []byte) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("reqtype", "fileupload"); err != nil {
		return "", fmt.Errorf("litterbox: write field: %w", err)
	}
	if err := w.WriteField("time", "1h"); err != nil {
		return "", fmt.Errorf("litterbox: write time: %w", err)
	}
	fw, err := w.CreateFormFile("fileToUpload", filename)
	if err != nil {
		return "", fmt.Errorf("litterbox: create form: %w", err)
	}
	if _, err := fw.Write(data); err != nil {
		return "", fmt.Errorf("litterbox: write data: %w", err)
	}
	w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 590*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, _litterboxURL, &buf)
	if err != nil {
		return "", fmt.Errorf("litterbox: create request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Close = true

	resp, err := newTmpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("litterbox: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("litterbox: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("litterbox: bad status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	url := strings.TrimSpace(string(body))
	if !strings.HasPrefix(url, "http") {
		return "", fmt.Errorf("litterbox: unexpected response: %s", url)
	}
	return url, nil
}

func uploadMhimg(filename string, data []byte) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	expired := time.Now().Add(time.Hour).Format("2006-01-02 15:04:05")
	if err := w.WriteField("expired_at", expired); err != nil {
		return "", fmt.Errorf("mhimg: write field: %w", err)
	}
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("mhimg: create form: %w", err)
	}
	if _, err := fw.Write(data); err != nil {
		return "", fmt.Errorf("mhimg: write data: %w", err)
	}
	w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 590*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, _mhimgURL, &buf)
	if err != nil {
		return "", fmt.Errorf("mhimg: create request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Close = true

	resp, err := newTmpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("mhimg: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("mhimg: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mhimg: bad status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Data struct {
			Links struct {
				URL string `json:"url"`
			} `json:"links"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("mhimg: parse json: %w", err)
	}
	if result.Data.Links.URL == "" {
		return "", fmt.Errorf("mhimg: empty url: %s", strings.TrimSpace(string(body)))
	}
	return result.Data.Links.URL, nil
}

func uploadScdnIo(filename string, data []byte) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("outputFormat", "auto"); err != nil {
		return "", fmt.Errorf("img.scdn.io: write field: %w", err)
	}
	fw, err := w.CreateFormFile("image", filename)
	if err != nil {
		return "", fmt.Errorf("img.scdn.io: create form: %w", err)
	}
	if _, err := fw.Write(data); err != nil {
		return "", fmt.Errorf("img.scdn.io: write data: %w", err)
	}
	w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 590*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, _scdnIoURL, &buf)
	if err != nil {
		return "", fmt.Errorf("img.scdn.io: create request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Close = true

	resp, err := newTmpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("img.scdn.io: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("img.scdn.io: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("img.scdn.io: bad status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		URL  string `json:"url"`
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("img.scdn.io: parse json: %w", err)
	}
	if result.URL != "" {
		return result.URL, nil
	}
	if result.Data.URL != "" {
		return result.Data.URL, nil
	}
	return "", fmt.Errorf("img.scdn.io: empty url: %s", strings.TrimSpace(string(body)))
}
