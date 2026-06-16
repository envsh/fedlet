package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
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

func init() {
	log.Println("toxrestsim: registering /api/* stub handlers")
	http.HandleFunc("/api/self", handleSelf)
	http.HandleFunc("/api/switchpeer", handleSwitchPeer)
	http.HandleFunc("/api/messages/send", handleMessageSend)
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

	writeJSON(w, map[string]interface{}{"message_id": e.ID})
}

func writeErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
