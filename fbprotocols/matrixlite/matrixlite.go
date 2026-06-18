package matrixlite

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
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

func parseAuth(auth string) (token, user, password string) {
	if auth == "" {
		return "", "", ""
	}
	if !strings.Contains(auth, ":") {
		return auth, "", ""
	}
	for i := len(auth) - 1; i >= 0; i-- {
		if auth[i] == ':' {
			return "", auth[:i], auth[i+1:]
		}
	}
	return "", "", ""
}

func Start(server, auth string) {
	baseURL, err := DiscoverBaseURL(server)
	if err != nil {
		switch {
		case errors.Is(err, ErrWellKnownNotFound):
			log.Printf("matrixlite: no well-known for %s, using as-is", server)
		default:
			log.Printf("matrixlite: well-known discovery for %s: %v; using as-is", server, err)
		}
	}
	log.Printf("matrixlite: using base URL %s (from %s)", baseURL, server)
	token, user, password := parseAuth(auth)
	go pollLoop(baseURL, token, user, password)
}

func pollLoop(baseURL, token, user, password string) {
	log.Printf("matrixlite: server=%s user=%s", baseURL, user)

	var state State
	state.Load()

	for {
		client := loginOrRestore(baseURL, token, user, password, &state)
		if client == nil {
			time.Sleep(10 * time.Second)
			continue
		}
		muClient.Lock()
		curClient = client
		muClient.Unlock()

		state.Server = baseURL
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

func loginOrRestore(baseURL, token, user, password string, state *State) *Client {
	c := &Client{
		baseURL: baseURL,
		hc: &http.Client{
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
		},
	}

	if state.Valid() && state.Server == baseURL {
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

	if token != "" {
		c, err := ClientFromToken(baseURL, token)
		if err != nil {
			log.Printf("matrixlite: token login error: %v", err)
			return nil
		}
		log.Printf("matrixlite: logged in with token (sliding=%v)", c.useSliding)
		return c
	}

	var err error
	c, err = Login(baseURL, user, password)
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
