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
	Capacities: ProtocolCapacities{CanReceive: true},
	statusFn: func() ProtocolStatus {
		return ProtocolStatus{
			Running:        outlookgraph.IsRunning(),
			LastErrs:       outlookgraph.LastErrs(),
			ConnectedSince: outlookgraph.ConnectedSince(),
			ReconnTimes:    outlookgraph.ReconnTimes(),
		}
	},
})

func init() {
	flag.StringVar(&outlookClientID, "outlook-client-id", "", "Azure AD app client ID (required)")
	starters = append(starters, func() {
		cfg := outlookgraph.Config{ClientID: outlookClientID}
		b, _ := json.Marshal(cfg)
		outlookgraph.SetPublishInfo(func(data []byte) error {
			return publish(channel_name, data)
		})
		outlookgraph.Start(string(b))
	})
}
