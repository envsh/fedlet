package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
)

type selfInfo struct {
	Address          string `json:"address"`
	Name             string `json:"name"`
	StatusMessage    string `json:"status_message"`
	ConnectionStatus int    `json:"connection_status"`
}

var (
	simSelf   = selfInfo{
		Address:          "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6",
		Name:             "fedbridge",
		StatusMessage:    "Online",
		ConnectionStatus: 1,
	}
	simMu sync.Mutex
)

func init() {
	log.Println("toxrestsim: registering /api/* stub handlers")
	http.HandleFunc("/api/self", handleSelf)
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
