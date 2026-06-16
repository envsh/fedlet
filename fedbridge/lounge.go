//go:build lounge
package main

import (
	"flag"

	"github.com/envsh/fedlet/fbprotocols/lounge"
)

var loungeServer string

func init() {
	lounge.SetPublishInfo(func(data []byte) error {
		return publish(channel_name, data)
	})
	RegisterSender("lounge", lounge.Send)
	flag.StringVar(&loungeServer, "lounge", "http://localhost:9000", "The Lounge server URL")
	starters = append(starters, func() {
		lounge.Start(loungeServer)
	})
}
