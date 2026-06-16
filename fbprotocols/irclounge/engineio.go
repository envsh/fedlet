package irclounge

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type EioPacketType byte

const (
	EioOpen    EioPacketType = 0
	EioClose   EioPacketType = 1
	EioPing    EioPacketType = 2
	EioPong    EioPacketType = 3
	EioMessage EioPacketType = 4
	EioUpgrade EioPacketType = 5
	EioNoop    EioPacketType = 6
)

type EioPacket struct {
	Type EioPacketType
	Data []byte
}

type eioHandshake struct {
	SID          string   `json:"sid"`
	PingInterval int      `json:"pingInterval"`
	PingTimeout  int      `json:"pingTimeout"`
	Upgrades     []string `json:"upgrades"`
}

type EioSession struct {
	baseURL      string
	sid          string
	pingInterval time.Duration
	pingTimeout  time.Duration
	hc           *http.Client
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	closed       bool
	buf          []EioPacket
}

func newPollingURL(base, sid string) string {
	q := url.Values{}
	q.Set("EIO", "4")
	q.Set("transport", "polling")
	if sid != "" {
		q.Set("sid", sid)
	}
	return base + "?" + q.Encode()
}

func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
		},
	}
}

func DialEio(serverURL string) (*EioSession, error) {
	u := serverURL
	if u[len(u)-1] != '/' {
		u += "/"
	}
	u += "socket.io/"

	ctx, cancel := context.WithCancel(context.Background())

	hc := newHTTPClient(30 * time.Second)
	resp, err := hc.Get(newPollingURL(u, ""))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("engine.io handshake: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("engine.io read: %w", err)
	}
	if len(body) == 0 || body[0] != '0' {
		cancel()
		return nil, fmt.Errorf("engine.io bad handshake: %s", string(body))
	}

	var hs eioHandshake
	if err := json.Unmarshal(body[1:], &hs); err != nil {
		cancel()
		return nil, fmt.Errorf("engine.io decode handshake: %w", err)
	}
	if hs.PingInterval <= 0 {
		hs.PingInterval = 25000
	}
	if hs.PingTimeout <= 0 {
		hs.PingTimeout = 20000
	}

	timeout := time.Duration(hs.PingInterval+hs.PingTimeout+5000) * time.Millisecond
	return &EioSession{
		baseURL:      u,
		sid:          hs.SID,
		pingInterval: time.Duration(hs.PingInterval) * time.Millisecond,
		pingTimeout:  time.Duration(hs.PingTimeout) * time.Millisecond,
		hc:           newHTTPClient(timeout),
		ctx:          ctx,
		cancel:       cancel,
	}, nil
}

func (s *EioSession) SID() string { return s.sid }

func (s *EioSession) PingInterval() time.Duration { return s.pingInterval }

func (s *EioSession) poll() ([]byte, error) {
	req, err := http.NewRequestWithContext(s.ctx, "GET", newPollingURL(s.baseURL, s.sid), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (s *EioSession) writeRaw(data []byte) error {
	req, err := http.NewRequestWithContext(s.ctx, "POST", newPollingURL(s.baseURL, s.sid), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	resp, err := s.hc.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func encodeTextPacket(p EioPacket) []byte {
	b := []byte{byte('0' + p.Type)}
	if len(p.Data) > 0 {
		b = append(b, p.Data...)
	}
	return b
}

func decodeTextPackets(data []byte) []EioPacket {
	if bytes.Contains(data, []byte{0x1e}) {
		parts := bytes.Split(data, []byte{0x1e})
		var packets []EioPacket
		for _, part := range parts {
			if len(part) == 0 {
				continue
			}
			packets = append(packets, decodeTextPackets(part)...)
		}
		return packets
	}
	s := string(data)
	var packets []EioPacket
	for len(s) > 0 {
		typ := s[0] - '0'
		s = s[1:]
		switch typ {
		case 0, 4, 5:
			packets = append(packets, EioPacket{Type: EioPacketType(typ), Data: []byte(s)})
			s = ""
		case 1, 2, 3, 6:
			packets = append(packets, EioPacket{Type: EioPacketType(typ)})
		}
	}
	return packets
}

func (s *EioSession) ReadPacket() (EioPacket, error) {
	for {
		if len(s.buf) > 0 {
			p := s.buf[0]
			s.buf = s.buf[1:]
			return p, nil
		}
		body, err := s.poll()
		if err != nil {
			return EioPacket{}, err
		}
		packets := decodeTextPackets(body)
		for _, p := range packets {
			switch p.Type {
			case EioPing:
				if err := s.writeRaw([]byte{byte('0' + EioPong)}); err != nil {
					return EioPacket{}, err
				}
			case EioClose:
				s.buf = nil
				return p, nil
			case EioMessage, EioUpgrade:
				s.buf = append(s.buf, p)
			}
		}
	}
}

func (s *EioSession) WritePacket(p EioPacket) error {
	return s.writeRaw(encodeTextPacket(p))
}

func (s *EioSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.cancel()
	s.writeRaw([]byte{byte('0' + EioClose)})
	return nil
}
