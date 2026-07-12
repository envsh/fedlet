//go:build gomuks

package main

import (
	"flag"

	"github.com/envsh/fedlet/fbprotocols/gomuks"
)

var gomuksInfo string

var _ = RegisterProtocol(&ProtocolInfo{
	Name:       "gomuks",
	Ctypes:     []string{TypeGomuksRoom},
	Capacities: ProtocolCapacities{CanSend: true, CanReceive: true},
	SendFn:     gomuks.Send,
	DlMediaFn:  gomuks.DownloadMedia,
	StartFn:    func() { gomuks.Start(gomuksInfo) },
	statusFn: func() ProtocolStatus {
		return ProtocolStatus{
			Running:        gomuks.IsRunning(),
			LastErrs:       gomuks.LastErrs(),
			ConnectedSince: gomuks.ConnectedSince(),
			ReconnTimes:    gomuks.ReconnTimes(),
		}
	},
})

func init() {
	gomuks.SetPublishInfo(func(data []byte) error {
		return publish(channel_name, data)
	})
	flag.StringVar(&gomuksInfo, "gomuks", "127.0.0.1:29325", "gomuks info")
}
