package main

import "net/http"

func init() {
	http.HandleFunc("/stfile/", handleStFile)
}

func handleStFile(w http.ResponseWriter, r *http.Request) {
	if syncDir == "" {
		http.NotFound(w, r)
		return
	}
	http.StripPrefix("/stfile/", http.FileServer(http.Dir(syncDir))).ServeHTTP(w, r)
}
