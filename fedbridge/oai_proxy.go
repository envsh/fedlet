package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func init() {
	http.HandleFunc("/v1/", handleOAIProxy)
}

func handleOAIProxy(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	token := strings.TrimPrefix(auth, "Bearer ")

	pipeIdx := strings.Index(token, "|")
	if pipeIdx < 0 {
		http.Error(w, `{"error":"missing target host in Authorization: expected Bearer <host>|<key>"}`,
			http.StatusBadRequest)
		return
	}

	target, err := url.Parse(token[:pipeIdx])
	if err != nil {
		http.Error(w, `{"error":"invalid target URL"}`,
			http.StatusBadRequest)
		return
	}

	r.Header.Set("Authorization", "Bearer "+token[pipeIdx+1:])
	r.Host = target.Host
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("oai_proxy: upstream %s%s: %v", target.Host, r.URL.Path, err)
		http.Error(w, `{"error":"upstream error"}`, http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}
