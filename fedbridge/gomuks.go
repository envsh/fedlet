//go:build gomuks
package main

import (
	"flag"

	"github.com/envsh/fedlet/fbprotocols/gomuks"
)

var gomuksInfo string

func init() {
	gomuks.SetPublishInfo(func(data []byte) error {
		return publish(channel_name, data)
	})
	flag.StringVar(&gomuksInfo, "gomuks", "", "gomuks info default 127.0.0.1:29325")
	starters = append(starters, func() {
		gomuks.Start(gomuksInfo)
	})
}
