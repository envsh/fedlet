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

func init() {
	misskey.SetPublishInfo(func(data []byte) error {
		return publish(channel_name, data)
	})
	RegisterSender(TypeMisskeyNote, misskey.Send)
	flag.StringVar(&misskeyHost, "misskey", "", "Misskey instance URL")
	flag.StringVar(&misskeyToken, "misskey-token", "", "Misskey API token")
	flag.StringVar(&misskeyTimeline, "misskey-timeline", "home", "Timeline: home/local/hybrid/global")
	starters = append(starters, func() {
		misskey.Start(misskeyHost, misskeyToken, misskeyTimeline)
	})
}
