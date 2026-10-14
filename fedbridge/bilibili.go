//go:build bilibili

package main

import (
	"flag"
	"time"

	"github.com/envsh/fedlet/fbprotocols/bilibili"
)

var (
	bilibiliHot          = true
	bilibiliNotify       = true
	bilibiliInterval     = 600 * time.Second
	bilibiliNotifyIntval = 60 * time.Second
)

func init() {
	flag.BoolVar(&bilibiliHot, "bilibili-hot", true, "publish hot board")
	flag.BoolVar(&bilibiliNotify, "bilibili-notify", true, "publish notifications")
	flag.DurationVar(&bilibiliInterval, "bilibili-interval", 600*time.Second, "hot board poll interval")
	flag.DurationVar(&bilibiliNotifyIntval, "bilibili-notify-interval", 60*time.Second, "notify poll interval")
}

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
