package main

import (
	_ "embed"
	"net/http"
)

//go:embed fedconfig.html
var fedconfigHTML string

func init() {
	http.HandleFunc("/fedlet/config", handleFedConfig)
}

func handleFedConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(fedconfigHTML))
}
