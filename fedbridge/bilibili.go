//go:build bilibili

package main

import (
	"time"

	"github.com/envsh/fedlet/fbprotocols/bilibili"
)

var (
	bilibiliHot          = true
	bilibiliNotify       = true
	bilibiliInterval     = 600 * time.Second
	bilibiliNotifyIntval = 60 * time.Second
)

var _ = RegisterProtocol(&ProtocolInfo{
	Name:       "bilibili",
	Ctypes:     []string{"bilibili"},
	Capacities: ProtocolCapacities{CanReceive: true},
	StartFn: func() {
		bilibili.SetPublishInfo(func(v any) error {
			return publish("bilibili", channel_name, v)
		})
		bilibili.Start(bilibiliHot, bilibiliNotify, bilibiliInterval, bilibiliNotifyIntval)
	},
	statusFn: func() ProtocolStatus {
		return ProtocolStatus{
			Running:        bilibili.Running(),
			AuthStatus:     bilibili.AuthStatus(),
			LastErrs:       bilibili.LastErrs(),
			ConnectedSince: bilibili.ConnectedSince(),
		}
	},
})