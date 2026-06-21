//go:build gomuks
package main

import (
	"flag"

	"github.com/envsh/fedlet/fbprotocols/gomuks"
)

var gomuksInfo string

var _ = RegisterProtocol("gomuks", ProtocolCapacities{CanSend: true, CanReceive: true}, func() ProtocolStatus {
	return ProtocolStatus{
		Running:        gomuks.IsRunning(),
		LastErrs:       gomuks.LastErrs(),
		ConnectedSince: gomuks.ConnectedSince(),
		ReconnTimes:    gomuks.ReconnTimes(),
	}
})

func init() {
	gomuks.SetPublishInfo(func(data []byte) error {
		return publish(channel_name, data)
	})
	RegisterSender(TypeGomuksRoom, gomuks.Send)
	flag.StringVar(&gomuksInfo, "gomuks", "127.0.0.1:29325", "gomuks info")
	starters = append(starters, func() {
		gomuks.Start(gomuksInfo)
	})
}
