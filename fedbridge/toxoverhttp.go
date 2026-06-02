//go:build toxoverhttp
package main

import (
	"github.com/envsh/fedlet/fbprotocols/toxoverhttp"
)

func init() {
	toxoverhttp.SetPublishInfo(func(data []byte) error {
		return publish(channel_name, data)
	})
	toxoverhttp.Start("")
}
