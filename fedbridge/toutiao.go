//go:build toutiao

package main

import (
	"flag"
	"time"

	"github.com/envsh/fedlet/fbprotocols/toutiao"
)

var (
	toutiaoHot   = true
	toutiaoNews  = true
	toutiaoIntv  = 600 * time.Second
	toutiaoNIntv = 120 * time.Second
)

func init() {
	flag.BoolVar(&toutiaoHot, "toutiao-hot", true, "publish hot board")
	flag.BoolVar(&toutiaoNews, "toutiao-news", true, "publish news feed")
	flag.DurationVar(&toutiaoIntv, "toutiao-interval", 600*time.Second, "hot board poll interval")
	flag.DurationVar(&toutiaoNIntv, "toutiao-notify-interval", 120*time.Second, "news poll interval")
}

var _ = RegisterProtocol(&ProtocolInfo{
	Name:       "toutiao",
	Ctypes:     []string{"toutiao"},
	Capacities: ProtocolCapacities{CanReceive: true},
	StartFn: func() {
		toutiao.SetPublishInfo(func(v any) error {
			return publish("toutiao", channel_name, v)
		})
		toutiao.Start(toutiaoHot, toutiaoNews, toutiaoIntv, toutiaoNIntv)
	},
	statusFn: func() ProtocolStatus {
		return ProtocolStatus{
			Running:        toutiao.IsRunning(),
			AuthStatus:     toutiao.AuthStatus(),
			LastErrs:       toutiao.LastErrs(),
			ConnectedSince: toutiao.ConnectedSince(),
		}
	},
})
