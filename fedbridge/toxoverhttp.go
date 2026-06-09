//go:build toxoverhttp
package main

import (
	"flag"

	"github.com/envsh/fedlet/fbprotocols/toxoverhttp"
)

var toxhsURL string

func init() {
	toxoverhttp.SetPublishInfo(func(data []byte) error {
		return publish(channel_name, data)
	})
	flag.StringVar(&toxhsURL, "toxhs", "http://127.0.0.1:8181", "toxoverhttp REST URL")
	starters = append(starters, func() {
		toxoverhttp.Start(toxhsURL)
	})
}
