package irccloud

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
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

const baseURL = "https://www.irccloud.com"

type Config struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func loadConfig(info string) *Config {
	cfg := &Config{
		Email:    os.Getenv("IRC_USER"),
		Password: os.Getenv("IRC_PASS"),
	}
	if info != "" {
		var over Config
		if err := json.Unmarshal([]byte(info), &over); err == nil {
			if over.Email != "" {
				cfg.Email = over.Email
			}
			if over.Password != "" {
				cfg.Password = over.Password
			}
		} else if strings.Contains(info, ":") {
			parts := strings.SplitN(info, ":", 2)
			if parts[0] != "" {
				cfg.Email = parts[0]
			}
			if len(parts) > 1 && parts[1] != "" {
				cfg.Password = parts[1]
			}
		}
	}
	return cfg
}

func Start(info string) {
	go poll_irccloud(info)
}

func getFormToken(client *http.Client) (string, error) {
	req, err := http.NewRequest("POST", baseURL+"/chat/auth-formtoken", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Length", "0")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var r struct {
		Success bool   `json:"success"`
		Token   string `json:"token"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("decode formtoken: %w, body=%s", err, string(body))
	}
	if !r.Success {
		return "", fmt.Errorf("get formtoken failed: %s", string(body))
	}
	return r.Token, nil
}

func doLogin(client *http.Client, email, password, token string) (string, error) {
	v := url.Values{}
	v.Set("token", token)
	v.Set("email", email)
	v.Set("password", password)

	req, err := http.NewRequest("POST", baseURL+"/chat/login", strings.NewReader(v.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("x-auth-formtoken", token)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var r struct {
		Success bool   `json:"success"`
		Session string `json:"session"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("decode login: %w, body=%s", err, string(body))
	}
	if !r.Success {
		return "", fmt.Errorf("login failed: %s", string(body))
	}
	return r.Session, nil
}

type streamState struct {
	lastEID      uint64
	streamID     string
	idleInterval time.Duration
	sessionKey   string
}

func processMessage(client *http.Client, data []byte, state *streamState) error {
	var h struct {
		Type string `json:"type"`
		EID  uint64 `json:"eid"`
	}
	if err := json.Unmarshal(data, &h); err != nil {
		return err
	}

	if h.EID > state.lastEID {
		state.lastEID = h.EID
	}

	switch h.Type {
	case "header":
		var msg struct {
			StreamID     string `json:"streamid"`
			IdleInterval int    `json:"idle_interval"`
		}
		json.Unmarshal(data, &msg)
		state.streamID = msg.StreamID
		if msg.IdleInterval > 0 {
			state.idleInterval = time.Duration(msg.IdleInterval) * time.Millisecond
		}

	case "oob_include":
		var msg struct {
			URL string `json:"url"`
		}
		json.Unmarshal(data, &msg)
		if msg.URL != "" {
			fetchOOB(client, msg.URL, state)
		}

	case "set_shard":
		var msg struct {
			Cookie string `json:"cookie"`
		}
		json.Unmarshal(data, &msg)
		if msg.Cookie != "" {
			state.sessionKey = msg.Cookie
		}

	case "idle":
	}

	return publish(data)
}

func fetchOOB(client *http.Client, oobURL string, state *streamState) {
	log.Println("fetching OOB include:", oobURL)
	resp, err := client.Get(oobURL)
	if err != nil {
		log.Println("OOB fetch error:", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Println("OOB status:", resp.Status)
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if err := processMessage(client, []byte(line), state); err != nil {
			log.Println("OOB process error:", err)
		}
	}
	if err := scanner.Err(); err != nil {
		log.Println("OOB scan error:", err)
	}
}

func connectAndStream(client *http.Client, sessionKey string, state *streamState) error {
	u := baseURL + "/chat/stream"
	if state.streamID != "" {
		q := url.Values{}
		q.Set("since_id", fmt.Sprintf("%d", state.lastEID))
		q.Set("stream_id", state.streamID)
		u += "?" + q.Encode()
	}

	log.Println("connecting to stream:", u)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Cookie", "session="+sessionKey)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("stream status %d: %s", resp.StatusCode, string(body))
	}

	log.Println("stream connected")
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if err := processMessage(client, []byte(line), state); err != nil {
			log.Println("stream process error:", err)
		}
	}
	return scanner.Err()
}

func poll_irccloud(info string) {
	authClient := &http.Client{Timeout: 30 * time.Second}
	var streamClient *http.Client

	for {
		cfg := loadConfig(info)
		if cfg.Email == "" || cfg.Password == "" {
			log.Println("IRCCloud: email/password not configured, retry in 30s")
			time.Sleep(30 * time.Second)
			continue
		}

		token, err := getFormToken(authClient)
		if err != nil {
			log.Println("IRCCloud formtoken error:", err)
			time.Sleep(10 * time.Second)
			continue
		}

		sessionKey, err := doLogin(authClient, cfg.Email, cfg.Password, token)
		if err != nil {
			log.Println("IRCCloud login error:", err)
			time.Sleep(10 * time.Second)
			continue
		}

		log.Println("IRCCloud authenticated")

		if streamClient == nil {
			streamClient = &http.Client{}
		}

		state := &streamState{sessionKey: sessionKey}
		for {
			if state.sessionKey != sessionKey {
				sessionKey = state.sessionKey
			}
			err := connectAndStream(streamClient, sessionKey, state)
			if err != nil {
				log.Println("IRCCloud stream error:", err)
			} else {
				log.Println("IRCCloud stream disconnected")
			}
			time.Sleep(5 * time.Second)
		}
	}
}
