//go:build gomuks
package main

import (
	 "github.com/envsh/fedlet/fbprotocols/gomuks"
)

func init() {
	gomuks.SetPublishInfo(func(data []byte) error {
		return publish(channel_name, data)
	})
	gomuks.Start("")
}
