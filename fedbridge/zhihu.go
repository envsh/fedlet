//go:build zhihu

package main

import (
	"time"

	"github.com/envsh/fedlet/fbprotocols/zhihu"
)

var (
	zhihuHot          = true
	zhihuNotify       = true
	zhihuInterval     = 600 * time.Second
	zhihuNotifyIntval = 60 * time.Second
)

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
