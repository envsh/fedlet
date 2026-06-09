//go:build outlookgraph
package main

import (
	"encoding/json"
	"flag"

	"github.com/envsh/fedlet/fbprotocols/outlookgraph"
)

var outlookClientID string

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
