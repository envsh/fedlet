//go:build coolapk

package main

import (
	"time"

	"github.com/envsh/fedlet/fbprotocols/coolapk"
)

var (
	coolapkHot   = true
	coolapkNews  = true
	coolapkIntv  = 600 * time.Second
	coolapkNIntv = 120 * time.Second
)

var _ = RegisterProtocol(&ProtocolInfo{
	Name:       "coolapk",
	Ctypes:     []string{"coolapk"},
	Capacities: ProtocolCapacities{CanReceive: true},
	StartFn: func() {
		coolapk.SetPublishInfo(func(v any) error {
			return publish("coolapk", channel_name, v)
		})
		coolapk.Start(coolapkHot, coolapkNews, coolapkIntv, coolapkNIntv)
	},
	statusFn: func() ProtocolStatus {
		return ProtocolStatus{
			Running:        coolapk.IsRunning(),
			AuthStatus:     coolapk.AuthStatus(),
			LastErrs:       coolapk.LastErrs(),
			ConnectedSince: coolapk.ConnectedSince(),
		}
	},
})