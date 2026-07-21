//go:build misskey

package main

import (
	"flag"

	"github.com/envsh/fedlet/fbprotocols/misskey"
)

var (
	misskeyHost     string
	misskeyToken    string
	misskeyTimeline string
)

var _ = RegisterProtocol(&ProtocolInfo{
	Name:       "misskey",
	Ctypes:     []string{TypeMisskeyNote},
	Capacities: ProtocolCapacities{CanSend: true, CanReceive: true},
	SendFn:     misskey.Send,
	StartFn:    func() { misskey.Start(misskeyHost, misskeyToken, misskeyTimeline) },
	statusFn: func() ProtocolStatus {
		return ProtocolStatus{
			Running:        misskey.IsRunning(),
			LastErrs:       misskey.LastErrs(),
			ConnectedSince: misskey.ConnectedSince(),
			ReconnTimes:    misskey.ReconnTimes(),
		}
	},
})

func init() {
	misskey.SetPublishInfo(func(v any) error {
		return publish("misskey", channel_name, v)
	})
	flag.StringVar(&misskeyHost, "misskey", "https://misskey.io", "Misskey instance URL")
	flag.StringVar(&misskeyToken, "misskey-token", "", "Misskey API token")
	flag.StringVar(&misskeyTimeline, "misskey-timeline", "home", "Timeline: home/local/hybrid/global")
}
