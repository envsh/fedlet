package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"sort"
	"strconv"

	"github.com/envsh/libp2px/p2put"
)

//go:embed fedlet.html
var fedletHTML string

type peerEntry struct {
	No   int    `json:"no"`
	ID   string `json:"id"`
	Name string `json:"name"`
	IP   string `json:"ip"`
}

var (
	currentPeerID string
	localPeerID   string
	localPeerIP   string
	localPeerIPv6 string
)

var ipv6Prefixes = []string{
	"fd00::",      // ULA mesh 内部
	"200::",       // Yggdrasil 废弃段，任意使用（RFC 4048）
	"240e::",      // ISP 中国电信
	"2001:470::",  // Hurricane Electric Tunnelbroker
	"2607:f8b0::", // Google
}

func getPeerList() []peerEntry {
	ids := p2put.GetClusterPeers()
	sort.Strings(ids)
	out := make([]peerEntry, 0, len(ids))
	for i, id := range ids {
		hostPart := stringToHostPart(id)
		ip := vlanpfx + strconv.Itoa(hostPart)
		out = append(out, peerEntry{No: i, ID: id, Name: id, IP: ip})
	}
	return out
}

func init() {
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
	pl := getPeerList()
	if r.Method == http.MethodPost {
		r.ParseForm()
		if p := r.FormValue("peerno"); p != "" {
			if n, err := strconv.Atoi(p); err == nil && n >= 0 && n < len(pl) {
				if driftsrv != nil {
					driftsrv.SwitchPeer(pl[n].ID)
				}
				currentPeerID = pl[n].ID
				log.Printf("fedlet: switched to peer %d (%s)", n, pl[n].ID)
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
	for i, p := range pl {
		if p.ID == currentPeerID {
			peerno = i
			break
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"peerno":   peerno,
		"peer":     currentPeerID,
		"peers":    pl,
		"local_id": localPeerID,
		"local_ip": localPeerIP,
	})
}
