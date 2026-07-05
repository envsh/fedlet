package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func init() {
	http.HandleFunc("/api/pastebin", handlePastebin)
}

var (
	_pasteRsURL   = "https://paste.rs/"
	_dpasteComURL = "https://dpaste.com/api/v2/"
)

type pastebinRequest struct {
	Text string `json:"text"`
}

type pastebinResponse struct {
	URL     string `json:"url,omitempty"`
	Service string `json:"service,omitempty"`
	Error   string `json:"error,omitempty"`
}

// POST /api/pastebin
//
// Upload text to a pastebin service (paste.rs or dpaste.com).
// The order of services is randomized; if one fails, the other is tried as fallback.
//
// Request:
//   POST /api/pastebin
//   Content-Type: application/json
//   {"text": "content to paste"}
//
// Response (200):
//   {"url": "https://paste.rs/abc123", "service": "paste.rs"}
//
// Response (502 — both services failed):
//   {"error": "all pastebin services failed: ..."}
//
// Response (400 — invalid request):
//   {"error": "text is required"}
func handlePastebin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req pastebinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, fmt.Sprintf("invalid json: %s", err), http.StatusBadRequest)
		return
	}
	if req.Text == "" {
		writeErr(w, "text is required", http.StatusBadRequest)
		return
	}

	uploaders := []struct {
		name string
		fn   func(string) (string, error)
	}{
		{"paste.rs", uploadPasteRs},
		{"dpaste.com", uploadDpasteCom},
	}

	perm := rand.Perm(len(uploaders))
	var firstErr error
	for _, i := range perm {
		url, err := uploaders[i].fn(req.Text)
		if err == nil {
			log.Printf("pastebin: uploaded via %s -> %s", uploaders[i].name, url)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(pastebinResponse{
				URL:     url,
				Service: uploaders[i].name,
			})
			return
		}
		log.Printf("pastebin: %s failed: %s", uploaders[i].name, err)
		if firstErr == nil {
			firstErr = err
		}
	}

	writeErr(w, fmt.Sprintf("all pastebin services failed: %s", firstErr), http.StatusBadGateway)
}

func newClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second}
}

func uploadPasteRs(text string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, _pasteRsURL, strings.NewReader(text))
	if err != nil {
		return "", fmt.Errorf("paste.rs: create request: %w", err)
	}
	req.Close = true
	req.Header.Set("User-Agent", "Fedlet/1.0")
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")

	resp, err := newClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("paste.rs: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("paste.rs: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("paste.rs: bad status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	u := strings.TrimSpace(string(body))
	if u == "" || !strings.HasPrefix(u, "http") {
		return "", fmt.Errorf("paste.rs: bad response: %q", u)
	}
	return u, nil
}

func uploadDpasteCom(text string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	form := url.Values{"content": {text}, "format": {"url"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, _dpasteComURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("dpaste.com: create request: %w", err)
	}
	req.Close = true
	req.Header.Set("User-Agent", "Fedlet/1.0")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := newClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("dpaste.com: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("dpaste.com: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("dpaste.com: bad status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	u := strings.TrimSpace(string(body))
	if u == "" || !strings.HasPrefix(u, "http") {
		return "", fmt.Errorf("dpaste.com: bad response: %q", u)
	}
	return u, nil
}
