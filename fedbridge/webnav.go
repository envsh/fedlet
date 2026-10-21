package main

import (
	_ "embed"
	"net/http"
)

//go:embed nav.js
var navJS string

func init() {
	http.HandleFunc("/fedlet/nav.js", func(w http.ResponseWriter, r *http.Request) {
		serveAsset(w, "nav.js", navJS, "application/javascript; charset=utf-8")
	})
}
