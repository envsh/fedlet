package irclounge

import (
	"encoding/json"
	"fmt"
)

type SioSession struct {
	eio *EioSession
}

func NewSioSession(eio *EioSession) *SioSession {
	return &SioSession{eio: eio}
}

type SioEvent struct {
	Name string
	Data []byte
}

func parseSioEvent(data []byte) (*SioEvent, error) {
	if len(data) < 2 || data[0] != '2' {
		return nil, fmt.Errorf("not a socket.io event packet")
	}
	payload := data[1:]
	if len(payload) > 0 && payload[0] == '/' {
		for i := 0; i < len(payload); i++ {
			if payload[i] == ',' {
				payload = payload[i+1:]
				break
			}
		}
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(payload, &arr); err != nil {
		return nil, fmt.Errorf("socket.io decode event: %w", err)
	}
	if len(arr) < 1 {
		return nil, fmt.Errorf("socket.io empty event array")
	}
	var name string
	if err := json.Unmarshal(arr[0], &name); err != nil {
		return nil, fmt.Errorf("socket.io decode event name: %w", err)
	}
	var rawData []byte
	if len(arr) > 1 {
		rawData = []byte(arr[1])
	}
	return &SioEvent{Name: name, Data: rawData}, nil
}

func (s *SioSession) ReadEvent() (*SioEvent, error) {
	for {
		pkt, err := s.eio.ReadPacket()
		if err != nil {
			return nil, err
		}
		if pkt.Type != EioMessage {
			continue
		}
		ev, err := parseSioEvent(pkt.Data)
		if err != nil {
			continue
		}
		return ev, nil
	}
}

func (s *SioSession) SendConnect() error {
	return s.eio.WritePacket(EioPacket{Type: EioMessage, Data: []byte("0")})
}

func (s *SioSession) Emit(event string, v interface{}) error {
	arr := []interface{}{event}
	if v != nil {
		arr = append(arr, v)
	}
	data, err := json.Marshal(arr)
	if err != nil {
		return fmt.Errorf("socket.io marshal: %w", err)
	}
	msg := make([]byte, 0, len(data)+1)
	msg = append(msg, '2')
	msg = append(msg, data...)
	return s.eio.WritePacket(EioPacket{Type: EioMessage, Data: msg})
}

func (s *SioSession) Close() error {
	return s.eio.Close()
}
