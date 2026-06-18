//go:build matrixlite
package main

import (
	"flag"

	"github.com/envsh/fedlet/fbprotocols/matrixlite"
)

var matrixURL, matrixAuth string

func init() {
	matrixlite.SetPublishInfo(func(data []byte) error {
		return publish(channel_name, data)
	})
	RegisterSender(TypeMatrix, matrixlite.Send)
	flag.StringVar(&matrixURL, "matrix-url", "", "Matrix server URL or domain (e.g. https://matrix.example.com or matrix.example.com)")
	flag.StringVar(&matrixAuth, "matrix-auth", "", "Matrix session token, or user:password (e.g. @user:example.com:pass)")
	starters = append(starters, func() {
		matrixlite.Start(matrixURL, matrixAuth)
	})
}
