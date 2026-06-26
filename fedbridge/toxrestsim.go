package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kitech/touse/oai"
)

type selfInfo struct {
	Address          string `json:"address"`
	Name             string `json:"name"`
	StatusMessage    string `json:"status_message"`
	ConnectionStatus int    `json:"connection_status"`
}

type simEvent struct {
	ID        uint64 `json:"id"`
	Type      string `json:"type"`
	Peer      string `json:"peer,omitempty"`
	Message   string `json:"message"`
	Timestamp int64  `json:"timestamp"`
}

var (
	simSelf   = selfInfo{
		Address:          "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6",
		Name:             "fedbridge",
		StatusMessage:    "Online",
		ConnectionStatus: 1,
	}
	simPeer  string
	simMu    sync.Mutex
	simEvents []simEvent
	simNextID uint64 = 1
)

type ServerInfo struct {
	BaseURL       string
	MSC3916Stable bool
	Versions      []string
	Features      map[string]bool
	LastChecked   time.Time
}

type AuthSupport int

const (
	AuthUnknown   AuthSupport = iota
	AuthSupported
	AuthUnsupported
)

var (
	serverCapsMu sync.Mutex
	serverCaps   = map[string]ServerInfo{}
)

func init() {
	log.Println("toxrestsim: registering /api/* stub handlers")
	http.HandleFunc("/api/self", handleSelf)
	http.HandleFunc("/api/switchpeer", handleSwitchPeer)
	http.HandleFunc("/api/messages/send", handleMessageSend)
	http.HandleFunc("/api/translate", handleTranslate)
	http.HandleFunc("/api/media_download", handleMediaDownload)
}

func handleTranslate(w http.ResponseWriter, r *http.Request) {
	type translateResponse struct {
		TranslatedText string `json:"translated_text,omitempty"`
		Error          string `json:"error,omitempty"`
		Code           string `json:"code,omitempty"`
	}
	writeTranslationErr := func(code, msg string, status int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(translateResponse{Error: msg, Code: code})
	}
	if r.Method != http.MethodPost {
		writeTranslationErr("INVALID_METHOD", "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Text string `json:"text"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeTranslationErr("INVALID_JSON", fmt.Sprintf("invalid json: %s", err), http.StatusBadRequest)
		return
	}
	if req.Text == "" || req.To == "" {
		writeTranslationErr("MISSING_FIELDS", "text and to required", http.StatusBadRequest)
		return
	}
	results, err := oai.MsetTranFull(req.To, "", req.Text)
	if err != nil {
		writeTranslationErr("TRANSLATE_FAILED", err.Error(), http.StatusInternalServerError)
		return
	}
	if len(results) == 0 {
		writeTranslationErr("TRANSLATE_FAILED", "translation returned empty result", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(translateResponse{TranslatedText: results[0]})
}

func queryWellKnown(ctx context.Context, server string) (string, error) {
	u := fmt.Sprintf("https://%s/.well-known/matrix/client", server)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "Fedlet/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf(".well-known returned %d", resp.StatusCode)
	}
	var wk struct {
		Homeserver struct{ BaseURL string `json:"base_url"` } `json:"m.homeserver"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wk); err != nil {
		return "", err
	}
	return strings.TrimRight(wk.Homeserver.BaseURL, "/"), nil
}

func queryVersions(ctx context.Context, baseURL string) ([]string, map[string]bool, error) {
	u := fmt.Sprintf("%s/_matrix/client/versions", strings.TrimRight(baseURL, "/"))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "Fedlet/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("versions returned %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	log.Printf("toxrestsim: versions body from %s: len=%d body=%s", baseURL, len(body), string(body))
	var data struct {
		Versions []string        `json:"versions"`
		Features map[string]bool `json:"unstable_features"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, nil, err
	}
	return data.Versions, data.Features, nil
}

func discoverServerInfo(server string) (ServerInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	wkBaseURL, wkErr := queryWellKnown(ctx, server)

	matrixURL := wkBaseURL
	if matrixURL == "" {
		matrixURL = "https://" + server
	}

	versions, features, vrErr := queryVersions(ctx, matrixURL)

	info := ServerInfo{
		BaseURL:       wkBaseURL,
		MSC3916Stable: features["org.matrix.msc3916.stable"],
		Versions:      versions,
		Features:      features,
		LastChecked:   time.Now(),
	}

	if wkErr != nil && vrErr != nil {
		return info, fmt.Errorf("well-known: %w, versions: %w", wkErr, vrErr)
	}
	return info, nil
}

func handleMediaDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw := r.FormValue("url")
	if raw == "" {
		writeErr(w, "missing url parameter", http.StatusBadRequest)
		return
	}
	const prefix = "mxc://"
	if !strings.HasPrefix(raw, prefix) {
		writeErr(w, "invalid mxc url", http.StatusBadRequest)
		return
	}
	rest := raw[len(prefix):]
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		writeErr(w, "invalid mxc url format", http.StatusBadRequest)
		return
	}
	server, mediaID := parts[0], parts[1]

	cap, ok := serverCaps[server]
	known := ok
	if !ok {
		info, err := discoverServerInfo(server)
		if err == nil {
			serverCapsMu.Lock()
			serverCaps[server] = info
			serverCapsMu.Unlock()
			log.Printf("toxrestsim: caps %s base=%q msc3916=%v", server, info.BaseURL, info.MSC3916Stable)
			known = true
		} else {
			log.Printf("toxrestsim: discover %s failed: %v", server, err)
		}
		cap = info
	}
	var as AuthSupport
	switch {
	case !known:
		as = AuthUnknown
	default:
		val, ok := cap.Features["org.matrix.msc3916.stable"]
		switch {
		case ok && val:
			as = AuthSupported
		case ok && !val:
			as = AuthUnsupported
		default:
			as = AuthUnknown
		}
	}
	base := "https://" + server
	if cap.BaseURL != "" {
		base = cap.BaseURL
	}
	httpURL := fmt.Sprintf("%s/_matrix/media/v3/download/%s/%s", base, server, mediaID)
	log.Printf("toxrestsim: media_download mxc=%q → http=%q", raw, httpURL)

	ctx := r.Context()
	proxyReq, err := http.NewRequestWithContext(ctx, http.MethodGet, httpURL, nil)
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	proxyReq.Header.Set("User-Agent", "Fedlet/1.0")

	client := &http.Client{Timeout: 130 * time.Second}
	resp, err := client.Do(proxyReq)
	if err != nil {
		writeErr(w, err.Error(), http.StatusBadGateway)
		return
	}

	// 200 优先返回：跳过一切 auth 判断
	// 之前 bug：200 的 body 被 resp.Body.Close() 关闭后走到 error 分支
	if resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	if !needsAuthForMedia(resp, as) {
		writeErr(w, "media not accessible", http.StatusNotFound)
		return
	}

	log.Printf("toxrestsim: media_download auth required for %s (status %d)", raw, resp.StatusCode)
	for _, ctype := range []string{TypeMatrix, TypeGomuksRoom} {
		info := ctypeRegistry[ctype]
		if info == nil || info.DlMediaFn == nil {
			continue
		}
		rc, ct, err := info.DlMediaFn(raw)
		if err != nil {
			log.Printf("toxrestsim: DlMediaFn %s: %v", info.Name, err)
			continue
		}
		defer rc.Close()
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		io.Copy(w, rc)
		return
	}
	writeErr(w, "media not accessible", http.StatusNotFound)
}

func needsAuthForMedia(resp *http.Response, as AuthSupport) bool {
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		return true
	}
	if as == AuthSupported {
		resp.Body.Close()
		return true
	}
	if resp.StatusCode != http.StatusNotFound {
		resp.Body.Close()
		return as == AuthUnknown
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") && !strings.HasPrefix(ct, "text/plain") {
		log.Printf("toxrestsim: 404 body content-type=%q, skipped body read", ct)
		resp.Body.Close()
		return as == AuthUnknown
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	log.Printf("toxrestsim: 404 body (len=%d): %.200s", len(body), string(body))
	if strings.Contains(string(body), "authentication is required") {
		return true
	}
	return as == AuthUnknown
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func handleSelf(w http.ResponseWriter, r *http.Request) {
	simMu.Lock()
	defer simMu.Unlock()

	if r.Method == http.MethodPost {
		r.ParseForm()
		simSelf.Name = r.FormValue("name")
		simSelf.StatusMessage = r.FormValue("status_message")
		log.Printf("toxrestsim: POST /api/self name=%q status=%q", simSelf.Name, simSelf.StatusMessage)
	}

	writeJSON(w, simSelf)
}

// /api/switchpeer?peer=fullidorsuffix
func handleSwitchPeer(w http.ResponseWriter, r *http.Request) {
	simMu.Lock()
	defer simMu.Unlock()

	if r.Method == http.MethodPost {
		r.ParseForm()
		if p := r.FormValue("peer"); p != "" {
			simPeer = p
			log.Printf("toxrestsim: POST /api/switchpeer peer=%q", simPeer)
		}
	}

	writeJSON(w, map[string]string{"peer": simPeer})
}

func handleMessageSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	chatType := r.FormValue("type")
	idStr := r.FormValue("id")
	message := r.FormValue("message")

	if chatType == "" || idStr == "" || message == "" {
		writeErr(w, "missing required parameters: type, id, message", http.StatusBadRequest)
		return
	}

	simMu.Lock()
	if len(simEvents) >= 512 {
		simEvents = simEvents[len(simEvents)-511:]
	}
	e := simEvent{
		ID:        simNextID,
		Type:      chatType,
		Peer:      idStr,
		Message:   message,
		Timestamp: time.Now().Unix(),
	}
	simNextID++
	simEvents = append(simEvents, e)
	simMu.Unlock()

	log.Printf("toxrestsim: POST /api/messages/send type=%q id=%q message=%q event_id=%d",
		chatType, idStr, message, e.ID)

	log.Printf("toxrestsim: dispatching type=%q id=%q message=%q", chatType, idStr, message)
	if err := DispatchSend(chatType, idStr, message, chatType); err != nil {
		log.Printf("toxrestsim: dispatch error: %v", err)
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]interface{}{"message_id": e.ID})
}

func writeErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
