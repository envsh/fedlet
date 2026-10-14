//go:build coolapk

package main

import (
	"flag"
	"time"

	"github.com/envsh/fedlet/fbprotocols/coolapk"
)

var (
	coolapkHot   = true
	coolapkNews  = true
	coolapkIntv  = 600 * time.Second
	coolapkNIntv = 120 * time.Second
)

func init() {
	flag.BoolVar(&coolapkHot, "coolapk-hot", true, "publish hot board")
	flag.BoolVar(&coolapkNews, "coolapk-news", true, "publish news feed")
	flag.DurationVar(&coolapkIntv, "coolapk-interval", 600*time.Second, "hot board poll interval")
	flag.DurationVar(&coolapkNIntv, "coolapk-notify-interval", 120*time.Second, "news poll interval")
}

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
