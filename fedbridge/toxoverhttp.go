//go:build toxoverhttp
package main

import (
	"flag"

	"github.com/envsh/fedlet/fbprotocols/toxoverhttp"
)

var toxhsURL string

var _ = RegisterProtocol("toxoverhttp", ProtocolCapacities{CanSend: true, CanReceive: true}, func() ProtocolStatus {
	return ProtocolStatus{
		Running:        toxoverhttp.IsRunning(),
		LastErrs:       toxoverhttp.LastErrs(),
		ConnectedSince: toxoverhttp.ConnectedSince(),
		ReconnTimes:    toxoverhttp.ReconnTimes(),
	}
})

func init() {
	toxoverhttp.SetPublishInfo(func(data []byte) error {
		return publish(channel_name, data)
	})
	RegisterSender(TypeToxFriend, toxoverhttp.Send)
	RegisterSender(TypeToxConference, toxoverhttp.Send)
	RegisterSender(TypeToxGroup, toxoverhttp.Send)
	flag.StringVar(&toxhsURL, "toxhs", "http://127.0.0.1:8181", "toxoverhttp REST URL")
	starters = append(starters, func() {
		toxoverhttp.Start(toxhsURL)
	})
}
