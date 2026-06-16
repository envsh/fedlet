package irclounge

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

type Client struct {
	sio    *SioSession
	Events chan Event
	mu     sync.Mutex
	closed bool
	done   chan struct{}
}

type authPerformData struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

func Connect(serverURL, user, password string) (*Client, error) {
	eio, err := DialEio(serverURL)
	if err != nil {
		return nil, fmt.Errorf("engine.io: %w", err)
	}

	sio := NewSioSession(eio)

	c := &Client{
		sio:    sio,
		Events: make(chan Event, 256),
		done:   make(chan struct{}),
	}

	if err := c.auth(user, password); err != nil {
		eio.Close()
		return nil, err
	}

	go c.readLoop()

	return c, nil
}

func (c *Client) auth(user, password string) error {
	if err := c.sio.SendConnect(); err != nil {
		return fmt.Errorf("socket.io connect: %w", err)
	}

	timeout := time.After(30 * time.Second)

	for {
		select {
		case <-timeout:
			return fmt.Errorf("auth timeout")
		default:
		}

		ev, err := c.sio.ReadEvent()
		if err != nil {
			return fmt.Errorf("read event during auth: %w", err)
		}

		switch ev.Name {
		case "auth:start":
			if err := c.sio.Emit("auth:perform", authPerformData{
				User:     user,
				Password: password,
			}); err != nil {
				return fmt.Errorf("auth:perform emit: %w", err)
			}

		case "auth:success":
			return nil

		case "auth:failed":
			var detail struct {
				Error string `json:"error"`
			}
			if len(ev.Data) > 0 {
				json.Unmarshal(ev.Data, &detail)
			}
			msg := "authentication failed"
			if detail.Error != "" {
				msg += ": " + detail.Error
			}
			return fmt.Errorf(msg)

		default:
			log.Printf("irclounge auth phase event: %s", ev.Name)
		}
	}
}

func (c *Client) readLoop() {
	defer close(c.Events)
	for {
		ev, err := c.sio.ReadEvent()
		if err != nil {
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			if closed {
				return
			}
			log.Printf("irclounge read error: %v", err)
			return
		}
		select {
		case c.Events <- Event{Type: ev.Name, Data: ev.Data}:
		case <-c.done:
			return
		}
	}
}

func (c *Client) SendMessage(channelID int, text string) error {
	data := map[string]interface{}{
		"target": channelID,
		"text":   text,
	}
	return c.sio.Emit("input", data)
}

func (c *Client) RunCommand(channelID int, text string) error {
	return c.SendMessage(channelID, text)
}

func (c *Client) Join(channel string) error {
	return c.sio.Emit("input", map[string]interface{}{
		"target": nil,
		"text":   "/join " + channel,
	})
}

func (c *Client) CreateNetwork(cfg NetworkConfig) error {
	return c.sio.Emit("network:new", cfg)
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	close(c.done)
	return c.sio.Close()
}

type msgEventRaw struct {
	Msg  Message `json:"msg"`
	Chan int     `json:"chan"`
}

func ParseMsgEvent(data []byte) (*Message, error) {
	var raw msgEventRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return &raw.Msg, nil
}
