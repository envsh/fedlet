package irccloud

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

func publish(data []byte) error {
	if pubfn_ == nil {
		return fmt.Errorf("pubfn not set")
	}
	return pubfn_(data)
}

var pubfn_ func([]byte) error

func SetPublishInfo(pubfn func([]byte) error) {
	pubfn_ = pubfn
}

type AppConfig struct {
	Email    string   `json:"email"`
	Password string   `json:"password"`
	Channels []string `json:"channels"`
}

func parseAppConfig(info string) *AppConfig {
	cfg := &AppConfig{
		Email:    os.Getenv("IRC_USER"),
		Password: os.Getenv("IRC_PASS"),
	}
	if info == "" {
		return cfg
	}

	if info[0] == '{' {
		var ac AppConfig
		if err := json.Unmarshal([]byte(info), &ac); err == nil {
			if ac.Email != "" {
				cfg.Email = ac.Email
			}
			if ac.Password != "" {
				cfg.Password = ac.Password
			}
			if len(ac.Channels) > 0 {
				cfg.Channels = ac.Channels
			}
			return cfg
		}
	}

	if strings.Contains(info, ":") {
		parts := strings.SplitN(info, ":", 2)
		cfg.Email = parts[0]
		rest := parts[1]
		if idx := strings.Index(rest, ","); idx >= 0 {
			cfg.Password = rest[:idx]
			for _, ch := range strings.Split(rest[idx+1:], ",") {
				ch = strings.TrimSpace(ch)
				if ch != "" {
					cfg.Channels = append(cfg.Channels, ch)
				}
			}
		} else {
			cfg.Password = rest
		}
	}
	return cfg
}

func Start(info string) {
	go poll_irccloud(info)
}

type streamState struct {
	lastEID    uint64
	streamID   string
	sessionKey string
	cidMap     map[int]bool
}

func (st *streamState) processMessage(msg []byte, cl *Client) {
	var h struct {
		Type string `json:"type"`
		EID  uint64 `json:"eid"`
	}
	json.Unmarshal(msg, &h)

	if h.EID > st.lastEID {
		st.lastEID = h.EID
	}

	switch h.Type {
	case "header":
		var hdr struct {
			StreamID string `json:"streamid"`
		}
		json.Unmarshal(msg, &hdr)
		st.streamID = hdr.StreamID
		st.cidMap = make(map[int]bool)

	case "makeserver":
		var srv struct {
			CID    int    `json:"cid"`
			Status string `json:"status"`
		}
		json.Unmarshal(msg, &srv)
		connected := srv.Status == "connected_ready" || srv.Status == "connected" || srv.Status == "connected_joining"
		st.cidMap[srv.CID] = connected

	case "status_changed":
		var sc struct {
			CID       int    `json:"cid"`
			NewStatus string `json:"new_status"`
		}
		json.Unmarshal(msg, &sc)
		connected := sc.NewStatus == "connected_ready" || sc.NewStatus == "connected" || sc.NewStatus == "connected_joining"
		st.cidMap[sc.CID] = connected

	case "oob_include":
		var oob struct {
			URL string `json:"url"`
		}
		json.Unmarshal(msg, &oob)
		if oob.URL != "" {
			oobCh, err := cl.FetchOOB(oob.URL)
			if err != nil {
				log.Println("OOB fetch error:", err)
			} else {
				for oobMsg := range oobCh {
					st.processMessage(oobMsg, cl)
					if err := publish(oobMsg); err != nil {
						log.Println("OOB publish error:", err)
					}
				}
			}
		}

	case "backlog_complete":
		log.Println("backlog complete, cids:", st.cidMap)

	case "set_shard":
		var sh struct {
			Cookie string `json:"cookie"`
		}
		json.Unmarshal(msg, &sh)
		if sh.Cookie != "" {
			st.sessionKey = sh.Cookie
		}

	case "idle":
	}

	if err := publish(msg); err != nil {
		log.Println("publish error:", err)
	}
}

func poll_irccloud(info string) {
	cfg := parseAppConfig(info)
	if cfg.Email == "" || cfg.Password == "" {
		log.Println("IRCCloud: IRC_USER/IRC_PASS not set")
		return
	}

	cl := NewClient()
	state := &streamState{}

	for {
		if state.sessionKey == "" {
			sk, err := cl.Authenticate(Config{Email: cfg.Email, Password: cfg.Password})
			if err != nil {
				log.Println("IRCCloud auth error:", err)
				time.Sleep(10 * time.Second)
				continue
			}
			state.sessionKey = sk
			log.Println("IRCCloud authenticated")
		}

		sinceID := ""
		if state.streamID != "" && state.lastEID > 0 {
			sinceID = fmt.Sprintf("%d", state.lastEID)
		}

		ch, err := cl.ConnectStream(state.sessionKey, sinceID, state.streamID)
		if err != nil {
			log.Println("IRCCloud stream error:", err)
			state.sessionKey = ""
			time.Sleep(5 * time.Second)
			continue
		}

		log.Println("IRCCloud stream connected")
		for msg := range ch {
			state.processMessage(msg, cl)
		}
		log.Println("IRCCloud stream disconnected")
		time.Sleep(5 * time.Second)
	}
}
