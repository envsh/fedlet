//go:build matrixlite
package main

import (
	"flag"

	"github.com/envsh/fedlet/fbprotocols/matrixlite"
)

var matrixServer string

func init() {
	matrixlite.SetPublishInfo(func(data []byte) error {
		return publish(channel_name, data)
	})
	RegisterSender("matrix", matrixlite.Send)
	flag.StringVar(&matrixServer, "matrix", "", "Matrix server URL with credentials (e.g. http://localhost:8008 user:pass)")
	starters = append(starters, func() {
		matrixlite.Start(matrixServer)
	})
}
