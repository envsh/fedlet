//go:build irccloud
package main

import (
	"flag"

	"github.com/envsh/fedlet/fbprotocols/irccloud"
)

var ircInfo string

func init() {
	irccloud.SetPublishInfo(func(data []byte) error {
		return publish(channel_name, data)
	})
	flag.StringVar(&ircInfo, "irc", "", "IRCCloud email:password")
	starters = append(starters, func() {
		irccloud.Start(ircInfo)
	})
}
