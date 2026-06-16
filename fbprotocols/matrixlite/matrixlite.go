package matrixlite

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

var (
	pubfn_   func([]byte) error
	muClient sync.Mutex
	curClient *Client
)

func SetPublishInfo(pubfn func([]byte) error) {
	pubfn_ = pubfn
}

func publish(data []byte) error {
	if pubfn_ == nil {
		return nil
	}
	return pubfn_(data)
}

func parseConfig(info string) *Config {
	cfg := &Config{
		Server: "http://localhost:8008",
	}
	if info == "" {
		return cfg
	}
	if info[0] == '{' {
		var c Config
		if err := json.Unmarshal([]byte(info), &c); err == nil {
			if c.Server != "" {
				cfg.Server = c.Server
			}
			if c.User != "" {
				cfg.User = c.User
			}
			if c.Password != "" {
				cfg.Password = c.Password
			}
			return cfg
		}
	}
	for i := len(info) - 1; i >= 0; i-- {
		if info[i] == ':' {
			cfg.User = info[:i]
			cfg.Password = info[i+1:]
			break
		}
	}
	return cfg
}

func Start(info string) {
	go pollLoop(info)
}

func pollLoop(info string) {
	cfg := parseConfig(info)
	log.Printf("matrixlite: server=%s user=%s", cfg.Server, cfg.User)

	var state State
	state.Load()

	for {
		client := loginOrRestore(cfg, &state)
		if client == nil {
			time.Sleep(10 * time.Second)
			continue
		}
		muClient.Lock()
		curClient = client
		muClient.Unlock()

		state.Server = cfg.Server
		client.SaveSyncState(&state)
		state.Save()

		for {
			events, err := client.Sync(30 * time.Second)
			if err != nil {
				log.Printf("matrixlite: sync error: %v", err)
				break
			}

			client.SaveSyncState(&state)
			state.Save()

			for _, ev := range events {
				var msg Message
				if json.Unmarshal(ev.Data, &msg) == nil && msg.Body != "" {
					log.Printf("matrixlite: <%s> %s: %s", msg.RoomID, msg.Sender, msg.Body)
				}
				if err := publish(ev.Data); err != nil {
					log.Printf("matrixlite: publish error: %v", err)
				}
			}
		}

		log.Println("matrixlite: disconnected, reconnecting in 5s")
		time.Sleep(5 * time.Second)
	}
}

func loginOrRestore(cfg *Config, state *State) *Client {
	c := &Client{
		baseURL: cfg.Server,
		hc: &http.Client{
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
		},
	}

	if state.Valid() && state.Server == cfg.Server {
		c.RestoreFromState(state)
		if _, err := c.Sync(0); err == nil {
			log.Printf("matrixlite: restored session for %s (sliding=%v)", c.userID, c.useSliding)
			return c
		}
		if c.refreshToken != "" {
			if rerr := c.TokenRefresh(); rerr == nil {
				log.Printf("matrixlite: token refreshed for %s", c.userID)
				c.SaveSyncState(state)
				state.Save()
				if _, err := c.Sync(0); err == nil {
					return c
				}
			}
		}
		log.Printf("matrixlite: session expired, re-logging in")
	}

	var err error
	c, err = Login(cfg.Server, cfg.User, cfg.Password)
	if err != nil {
		log.Printf("matrixlite: login error: %v", err)
		return nil
	}
	log.Printf("matrixlite: logged in as %s (sliding=%v)", c.userID, c.useSliding)
	return c
}

func Send(roomID, msg, msgType string) error {
	if roomID == "" || msg == "" {
		return fmt.Errorf("matrixlite: empty roomID or message")
	}
	muClient.Lock()
	c := curClient
	muClient.Unlock()
	if c == nil {
		return fmt.Errorf("matrixlite: no active session")
	}
	return c.SendMessage(roomID, msg)
}
