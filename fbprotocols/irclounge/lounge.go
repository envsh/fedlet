package irclounge

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"
)

var (
	pubfn_      func([]byte) error
	muLounge    sync.Mutex
	ircloungeClient *Client
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

type Config struct {
	Server   string `json:"server"`
	User     string `json:"user"`
	Password string `json:"password"`
}

func parseConfig(info string) *Config {
	cfg := &Config{
		Server: "http://localhost:9000",
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
	parts := split2(info, ":")
	if len(parts) >= 2 {
		cfg.User = parts[0]
		cfg.Password = parts[1]
	}
	return cfg
}

func split2(s, sep string) []string {
	for i := 0; i < len(s)-len(sep); i++ {
		if s[i:i+len(sep)] == sep {
			return []string{s[:i], s[i+len(sep):]}
		}
	}
	return nil
}

func Start(info string) {
	go pollLounge(info)
}

func pollLounge(info string) {
	cfg := parseConfig(info)
	log.Printf("irclounge: server=%s user=%s", cfg.Server, cfg.User)

	for {
		client, err := Connect(cfg.Server, cfg.User, cfg.Password)
		if err != nil {
			log.Printf("irclounge: connect error: %v", err)
			time.Sleep(10 * time.Second)
			continue
		}
		muLounge.Lock()
		ircloungeClient = client
		muLounge.Unlock()
		log.Println("irclounge: connected")

		for event := range client.Events {
			switch event.Type {
			case "msg":
				msg, err := ParseMsgEvent(event.Data)
				if err != nil {
					log.Printf("irclounge: parse msg error: %v", err)
				} else {
					from := ""
					if msg.From != nil {
						from = msg.From.Nick
					}
					log.Printf("irclounge: <%s> %s", from, msg.Text)
				}
				if err := publish(event.Data); err != nil {
					log.Printf("irclounge: publish error: %v", err)
				}

			case "init":
				log.Println("irclounge: initial state loaded")

			case "network:status", "network", "network:name":
				log.Printf("irclounge: network event %s", event.Type)

			case "join", "part", "quit", "nick", "topic":
				log.Printf("irclounge: %s %s", event.Type, string(event.Data))

			case "channel:state", "names", "users":
				log.Printf("irclounge: channel event %s", event.Type)

			default:
				log.Printf("irclounge: event %s", event.Type)
			}
		}

		muLounge.Lock()
		ircloungeClient = nil
		muLounge.Unlock()
		log.Println("irclounge: disconnected, reconnecting in 5s")
		client.Close()
		time.Sleep(5 * time.Second)
	}
}

func Send(to, msg, msgType string) error {
	if to == "" || msg == "" {
		return fmt.Errorf("irclounge: empty target or message")
	}
	muLounge.Lock()
	cl := ircloungeClient
	muLounge.Unlock()
	if cl == nil {
		return fmt.Errorf("irclounge: not connected")
	}
	channelID, err := strconv.Atoi(to)
	if err != nil {
		return fmt.Errorf("irclounge: invalid channel ID %q: %w", to, err)
	}
	return cl.SendMessage(channelID, msg)
}
