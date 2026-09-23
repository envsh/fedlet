//go:build hongguo

package main

import (
	"time"

	"github.com/envsh/fedlet/fbprotocols/hongguo"
)

var (
	hongguoInterval = 600 * time.Second
)

var _ = RegisterProtocol(&ProtocolInfo{
	Name:       "hongguo",
	Ctypes:     []string{"hongguo"},
	Capacities: ProtocolCapacities{CanReceive: true},
	StartFn: func() {
		hongguo.SetPublishInfo(func(v any) error {
			return publish("hongguo", channel_name, v)
		})
		hongguo.Start(hongguoInterval)
	},
	statusFn: func() ProtocolStatus {
		return ProtocolStatus{
			Running:        hongguo.IsRunning(),
			AuthStatus:     hongguo.AuthStatus(),
			LastErrs:       hongguo.LastErrs(),
			ConnectedSince: hongguo.ConnectedSince(),
		}
	},
})
