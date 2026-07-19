//go:build outlookgraph

package main

import (
	"encoding/json"
	"flag"

	"github.com/envsh/fedlet/fbprotocols/outlookgraph"
)

var outlookClientID string

var _ = RegisterProtocol(&ProtocolInfo{
	Name:       "outlookgraph",
	Ctypes:     []string{TypeOutlookEvent},
	Capacities: ProtocolCapacities{CanReceive: true, CanSend: true},
	SendFn:     outlookgraph.Send,
	StartFn: func() {
		cfg := outlookgraph.Config{ClientID: outlookClientID}
		b, _ := json.Marshal(cfg)
		outlookgraph.SetPublishInfo(func(v any) error {
			return publish(channel_name, v)
		})
		outlookgraph.Start(string(b))
	},
	statusFn: func() ProtocolStatus {
		return ProtocolStatus{
			Running:        outlookgraph.IsRunning(),
			AuthStatus:     outlookgraph.AuthStatus(),
			LastErrs:       outlookgraph.LastErrs(),
			ConnectedSince: outlookgraph.ConnectedSince(),
			ReconnTimes:    outlookgraph.ReconnTimes(),
		}
	},
})

func init() {
	flag.StringVar(&outlookClientID, "outlook-client-id", "", "Azure AD app client ID (required)")
}
