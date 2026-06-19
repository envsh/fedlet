package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"strconv"
)

//go:embed fedlet.html
var fedletHTML string

type peerEntry struct {
	No   int    `json:"no"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

var peerList = []peerEntry{
	{0, peerid0, "peer0"},
	{1, peerid1, "peer1"},
	{2, peerid2, "peer2"},
	{3, peerid3, "peer3"},
	{4, peerid4, "peer4"},
}

var currentPeerID string

func init() {
	switch usepeer {
	case 4:
		currentPeerID = peerid4
	case 3:
		currentPeerID = peerid3
	case 2:
		currentPeerID = peerid0
	case 1:
		currentPeerID = peerid1
	default:
		currentPeerID = peerid2
	}

	http.HandleFunc("/fedlet/index", handleFedletIndex)
	http.HandleFunc("/api/protocols", handleProtocols)
	http.HandleFunc("/api/peer", handlePeer)
}

func handleFedletIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(fedletHTML))
}

type protoParam struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Hide  bool   `json:"hide"`
}

type protoInfo struct {
	Name   string       `json:"name"`
	Label  string       `json:"label"`
	Params []protoParam `json:"params"`
}

var knownProtocols = []struct {
	Flag   string
	Name   string
	Label  string
	Params []struct {
		Flag string
		Key  string
		Hide bool
	}
}{
	{"emailauth", "emailimap", "Email IMAP", []struct {
		Flag string
		Key  string
		Hide bool
	}{
		{"imapserver", "server", false},
		{"imapdir", "folders", false},
		{"emailauth", "auth", true},
	}},
	{"outlook-client-id", "outlookgraph", "Microsoft Graph", []struct {
		Flag string
		Key  string
		Hide bool
	}{
		{"outlook-client-id", "client_id", true},
	}},
	{"gomuks", "gomuks", "Matrix (gomuks)", []struct {
		Flag string
		Key  string
		Hide bool
	}{
		{"gomuks", "endpoint", false},
	}},
	{"irc", "irccloud", "IRCCloud", []struct {
		Flag string
		Key  string
		Hide bool
	}{
		{"irc-join", "channels", false},
		{"irc", "auth", true},
	}},
	{"irclounge", "irclounge", "The Lounge IRC", []struct {
		Flag string
		Key  string
		Hide bool
	}{
		{"irclounge", "server", false},
		{"irclounge-join", "channels", false},
		{"irclounge-network", "network", false},
		{"irclounge-auth", "auth", true},
	}},
	{"toxhs", "toxoverhttp", "Tox over HTTP", []struct {
		Flag string
		Key  string
		Hide bool
	}{
		{"toxhs", "url", false},
	}},
	{"matrix-url", "matrixlite", "Matrix (native)", []struct {
		Flag string
		Key  string
		Hide bool
	}{
		{"matrix-url", "server", false},
		{"matrix-auth", "auth", true},
	}},
	{"misskey", "misskey", "Misskey", []struct {
		Flag string
		Key  string
		Hide bool
	}{
		{"misskey", "instance", false},
		{"misskey-timeline", "timeline", false},
		{"misskey-token", "token", true},
	}},
}

func handleProtocols(w http.ResponseWriter, r *http.Request) {
	var out []protoInfo
	for _, p := range knownProtocols {
		if flag.Lookup(p.Flag) == nil {
			continue
		}
		info := protoInfo{Name: p.Name, Label: p.Label}
		for _, param := range p.Params {
			f := flag.Lookup(param.Flag)
			if f == nil {
				continue
			}
			v := f.Value.String()
			if param.Hide && v != "" {
				v = "******"
			}
			info.Params = append(info.Params, protoParam{Key: param.Key, Value: v, Hide: param.Hide && v != ""})
		}
		out = append(out, info)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func handlePeer(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		r.ParseForm()
		if p := r.FormValue("peerno"); p != "" {
			if n, err := strconv.Atoi(p); err == nil && n >= 0 && n < len(peerList) {
				if driftsrv != nil {
					driftsrv.SwitchPeer(peerList[n].ID)
				}
				currentPeerID = peerList[n].ID
				log.Printf("fedlet: switched to peer %d (%s)", n, peerList[n].ID)
			}
		} else if p := r.FormValue("peer"); p != "" {
			if driftsrv != nil {
				driftsrv.SwitchPeer(p)
			}
			currentPeerID = p
			log.Printf("fedlet: switched to peer %s", p)
		}
	}

	peerno := -1
	for i, p := range peerList {
		if p.ID == currentPeerID {
			peerno = i
			break
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"peerno": peerno,
		"peer":   currentPeerID,
		"peers":  peerList,
	})
}
