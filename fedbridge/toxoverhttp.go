//go:build toxoverhttp

package main

import (
	"flag"

	"github.com/envsh/fedlet/fbprotocols/toxoverhttp"
)

var toxhsURL string

var _ = RegisterProtocol(&ProtocolInfo{
	Name:       "toxoverhttp",
	Ctypes:     []string{TypeToxFriend, TypeToxConference, TypeToxGroup},
	Capacities: ProtocolCapacities{CanSend: true, CanReceive: true},
	SendFn:     toxoverhttp.Send,
	StartFn:    func() { toxoverhttp.Start(toxhsURL) },
	statusFn: func() ProtocolStatus {
		return ProtocolStatus{
			Running:        toxoverhttp.IsRunning(),
			LastErrs:       toxoverhttp.LastErrs(),
			ConnectedSince: toxoverhttp.ConnectedSince(),
			ReconnTimes:    toxoverhttp.ReconnTimes(),
		}
	},
})

func init() {
	toxoverhttp.SetPublishInfo(func(v any) error {
		return publish("toxoverhttp", channel_name, v)
	})
	flag.StringVar(&toxhsURL, "toxhs", "http://127.0.0.1:8181", "toxoverhttp REST URL")
}
