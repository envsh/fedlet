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
	flag.StringVar(&toxhsURL, "toxhs", "", "toxoverhttp REST URL")
	starters = append(starters, func() {
		toxoverhttp.Start(toxhsURL)
	})
}
