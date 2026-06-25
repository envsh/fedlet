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

func discoverServerInfo(server string) *ServerInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	type wkResp struct{ baseURL string }
	type vrResp struct {
		versions []string
		features map[string]bool
	}
	chWK := make(chan wkResp, 1)
	chVR := make(chan vrResp, 1)

	go func() {
		u := fmt.Sprintf("https://%s/.well-known/matrix/client", server)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("User-Agent", "Fedlet/1.0")
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			chWK <- wkResp{}
			return
		}
		defer resp.Body.Close()
		var wk struct {
			Homeserver struct{ BaseURL string } `json:"m.homeserver"`
		}
		json.NewDecoder(resp.Body).Decode(&wk)
		chWK <- wkResp{baseURL: strings.TrimRight(wk.Homeserver.BaseURL, "/")}
	}()

	go func() {
		u := fmt.Sprintf("https://%s/_matrix/client/versions", server)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("User-Agent", "Fedlet/1.0")
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			chVR <- vrResp{}
			return
		}
		defer resp.Body.Close()
		var data struct {
			Versions []string        `json:"versions"`
			Features map[string]bool `json:"unstable_features"`
		}
		json.NewDecoder(resp.Body).Decode(&data)
		chVR <- vrResp{versions: data.Versions, features: data.Features}
	}()

	wk := <-chWK
	vr := <-chVR

	baseURL := "https://" + server
	if wk.baseURL != "" {
		baseURL = wk.baseURL
	}

	info := &ServerInfo{
		BaseURL:       baseURL,
		MSC3916Stable: vr.features["org.matrix.msc3916.stable"],
		Versions:      vr.versions,
		Features:      vr.features,
		LastChecked:   time.Now(),
	}

	serverCapsMu.Lock()
	serverCaps[server] = *info
	serverCapsMu.Unlock()

	log.Printf("toxrestsim: caps %s base=%q msc3916=%v", server, baseURL, info.MSC3916Stable)
	return info
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

	serverCapsMu.Lock()
	cap, cached := serverCaps[server]
	serverCapsMu.Unlock()

	base := "https://" + server
	if cached && cap.BaseURL != "" {
		base = cap.BaseURL
	}
	httpURL := fmt.Sprintf("%s/_matrix/media/v3/download/%s/%s", base, server, mediaID)
	log.Printf("toxrestsim: media_download mxc=%q → http=%q (cached=%v)", raw, httpURL, cached)

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

	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		info := discoverServerInfo(server)
		cap = *info
		cached = true
		if info.BaseURL != base {
			httpURL = fmt.Sprintf("%s/_matrix/media/v3/download/%s/%s", info.BaseURL, server, mediaID)
			log.Printf("toxrestsim: media_download retry mxc=%q → http=%q", raw, httpURL)
			proxyReq, _ = http.NewRequestWithContext(ctx, http.MethodGet, httpURL, nil)
			proxyReq.Header.Set("User-Agent", "Fedlet/1.0")
			resp, err = client.Do(proxyReq)
			if err != nil {
				writeErr(w, err.Error(), http.StatusBadGateway)
				return
			}
		}
	}

	if resp.StatusCode != http.StatusOK && cap.MSC3916Stable {
		resp.Body.Close()
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
		return
	}

	defer resp.Body.Close()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func isAuthRequired(resp *http.Response) bool {
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return true
	}
	if resp.StatusCode != http.StatusNotFound {
		return false
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") && !strings.HasPrefix(ct, "text/plain") {
		return false
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), "authentication is required") {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return true
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return false
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
