//go:build zhihu

package main

import (
	"flag"
	"time"

	"github.com/envsh/fedlet/fbprotocols/zhihu"
)

var (
	zhihuHot          = true
	zhihuNotify       = true
	zhihuInterval     = 600 * time.Second
	zhihuNotifyIntval = 60 * time.Second
)

func init() {
	flag.BoolVar(&zhihuHot, "zhihu-hot", true, "publish hot board")
	flag.BoolVar(&zhihuNotify, "zhihu-notify", true, "publish notifications")
	flag.DurationVar(&zhihuInterval, "zhihu-interval", 600*time.Second, "hot board poll interval")
	flag.DurationVar(&zhihuNotifyIntval, "zhihu-notify-interval", 60*time.Second, "notify poll interval")
}

var _ = RegisterProtocol(&ProtocolInfo{
	Name:       "zhihu",
	Ctypes:     []string{"zhihu"},
	Capacities: ProtocolCapacities{CanReceive: true},
	StartFn: func() {
		zhihu.SetPublishInfo(func(v any) error {
			return publish("zhihu", channel_name, v)
		})
		zhihu.Start(zhihuHot, zhihuNotify, zhihuInterval, zhihuNotifyIntval)
	},
	statusFn: func() ProtocolStatus {
		return ProtocolStatus{
			Running:        zhihu.IsRunning(),
			AuthStatus:     zhihu.AuthStatus(),
			LastErrs:       zhihu.LastErrs(),
			ConnectedSince: zhihu.ConnectedSince(),
		}
	},
})
