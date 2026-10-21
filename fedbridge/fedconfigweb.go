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
	serveAsset(w, "fedconfig.html", fedconfigHTML, "text/html; charset=utf-8")
}
