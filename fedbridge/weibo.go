//go:build weibo

package main

import (
	"time"

	"github.com/envsh/fedlet/fbprotocols/weibo"
)

var (
	weiboInterval = 60 * time.Second
)

var _ = RegisterProtocol(&ProtocolInfo{
	Name:       "weibo",
	Ctypes:     []string{"weibo"},
	Capacities: ProtocolCapacities{CanReceive: true},
	StartFn: func() {
		weibo.SetPublishInfo(func(v any) error {
			return publish("weibo", channel_name, v)
		})
		weibo.Start(weiboInterval)
	},
	statusFn: func() ProtocolStatus {
		return ProtocolStatus{
			Running:        weibo.IsRunning(),
			AuthStatus:     weibo.AuthStatus(),
			LastErrs:       weibo.LastErrs(),
			ConnectedSince: weibo.ConnectedSince(),
		}
	},
})