//go:build matrixlite
package main

import (
	"flag"

	"github.com/envsh/fedlet/fbprotocols/matrixlite"
)

var matrixURL, matrixAuth string

var _ = RegisterProtocol(&ProtocolInfo{
	Name:       "matrixlite",
	Ctypes:     []string{TypeMatrix},
	Capacities: ProtocolCapacities{CanSend: true, CanReceive: true},
	SendFn:     matrixlite.Send,
	StartFn:    func() { matrixlite.Start(matrixURL, matrixAuth) },
	statusFn: func() ProtocolStatus {
		return ProtocolStatus{
			Running:        matrixlite.IsRunning(),
			LastErrs:       matrixlite.LastErrs(),
			ConnectedSince: matrixlite.ConnectedSince(),
			ReconnTimes:    matrixlite.ReconnTimes(),
		}
	},
})

func init() {
	matrixlite.SetPublishInfo(func(data []byte) error {
		return publish(channel_name, data)
	})
	flag.StringVar(&matrixURL, "matrix-url", "", "Matrix server URL or domain (e.g. https://matrix.example.com or matrix.example.com)")
	flag.StringVar(&matrixAuth, "matrix-auth", "", "Matrix session token, or user:password (e.g. @user:example.com:pass)")
}
