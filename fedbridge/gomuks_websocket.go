package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

const gomuksHost = "127.0.0.1:29325"

func poll_gomuks() {
	for {
		username := os.Getenv("GOMUKS_USER")
		password := os.Getenv("GOMUKS_PASS")
		if username == "" || password == "" {
			log.Println("GOMUKS_USER/GOMUKS_PASS not set, retry in 30s")
			time.Sleep(30 * time.Second)
			continue
		}

		authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
		token := doGomuksAuth(authHeader)
		if token == "" {
			time.Sleep(10 * time.Second)
			continue
		}

		header := http.Header{}
		header.Set("Cookie", "gomuks_auth=" + token)

		u := fmt.Sprintf("ws://%s/_gomuks/websocket", gomuksHost)
		c, resp, err := websocket.DefaultDialer.Dial(u, header)
		if err != nil {
			if resp != nil {
				log.Println("ws dial error: status=", resp.Status, ", err=", err)
			} else {
				log.Println("ws dial error:", err)
			}
			time.Sleep(5 * time.Second)
			continue
		}
		if resp != nil {
			log.Println("ws dial: http status=", resp.Status)
		}

		log.Println("ws connected")
		gomuksEventLoop(c)
		log.Println("ws disconnected, reconnecting...")
		time.Sleep(5 * time.Second)
	}
}

func doGomuksAuth(authHeader string) string {
	url := fmt.Sprintf("http://%s/_gomuks/auth?output=json", gomuksHost)
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		log.Println("auth request error:", err)
		return ""
	}
	req.Header.Set("Authorization", authHeader)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Println("auth error:", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		log.Println("auth disabled (204)")
		return ""
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		log.Println("auth failed:", resp.Status, string(body))
		return ""
	}

	var ar struct{ Token string `json:"token"` }
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		log.Println("auth decode error:", err)
		return ""
	}
	return ar.Token
}

func gomuksEventLoop(c *websocket.Conn) {
	defer c.Close()

	var lastReceivedID int
	var seq int
	pingTicker := time.NewTicker(15 * time.Second)
	defer pingTicker.Stop()

	msgCh := make(chan []byte, 64)

	go func() {
		for {
			_, msg, err := c.ReadMessage()
			if err != nil {
				log.Println("ws read error:", err)
				close(msgCh)
				return
			}
			msgCh <- msg
		}
	}()

	for {
		select {
		case msg, ok := <-msgCh:
			if !ok {
				return
			}
			if err := publish(channel_name, msg); err != nil {
				log.Println("publish error:", err)
			}

		case <-pingTicker.C:
			seq++
			ping := map[string]any{
				"command":    "ping",
				"request_id": seq,
				"data": map[string]any{
					"last_received_id": lastReceivedID,
				},
			}
			data, _ := json.Marshal(ping)
			if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
				log.Println("ping error:", err)
				return
			}
		}
	}
}
