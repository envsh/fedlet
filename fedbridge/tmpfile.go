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
