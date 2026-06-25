package main

import (
	"io"
	"time"
)

type ProtocolCapacities struct {
	CanSend    bool
	CanReceive bool
}

type ProtocolStatus struct {
	Running        bool
	LastErrs       []error
	ConnectedSince time.Time
	ReconnTimes    int64
}

type ProtocolInfo struct {
	Name       string
	Ctypes     []string
	Capacities ProtocolCapacities
	SendFn     func(to, msg, msgType string) error
	// DlMediaFn downloads media identified by an mxc:// URL.
	// Caller must close the returned io.ReadCloser.
	DlMediaFn func(mxcURL string) (io.ReadCloser, string, error)
	StartFn    func()
	StopFn     func()
	statusFn   func() ProtocolStatus
}

func (p *ProtocolInfo) Status() ProtocolStatus {
	if p.statusFn == nil {
		return ProtocolStatus{}
	}
	return p.statusFn()
}

var ctypeRegistry = make(map[string]*ProtocolInfo)

func RegisterProtocol(info *ProtocolInfo) *ProtocolInfo {
	for _, ctype := range info.Ctypes {
		ctypeRegistry[ctype] = info
	}
	return info
}

func ProtocolStatuses() []*ProtocolInfo {
	seen := make(map[string]bool)
	var out []*ProtocolInfo
	for _, info := range ctypeRegistry {
		if seen[info.Name] {
			continue
		}
		seen[info.Name] = true
		out = append(out, info)
	}
	return out
}

func ProtocolByName(name string) *ProtocolInfo {
	for _, info := range ctypeRegistry {
		if info.Name == name {
			return info
		}
	}
	return nil
}
