package main

import (
	_ "embed"
	"net/http"
)

//go:embed nav.js
var navJS string

func init() {
	http.HandleFunc("/fedlet/nav.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		_, _ = w.Write([]byte(navJS))
	})
}
