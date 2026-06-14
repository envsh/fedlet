package matrixlite

import (
	"encoding/json"
	"log"
	"time"
)

var pubfn_ func([]byte) error

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

	for {
		client, err := Login(cfg.Server, cfg.User, cfg.Password)
		if err != nil {
			log.Printf("matrixlite: login error: %v", err)
			time.Sleep(10 * time.Second)
			continue
		}
		log.Printf("matrixlite: logged in as %s (sliding=%v)", client.userID, client.useSliding)

		for {
			events, err := client.Sync(30 * time.Second)
			if err != nil {
				log.Printf("matrixlite: sync error: %v", err)
				break
			}
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
