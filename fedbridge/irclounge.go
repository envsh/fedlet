//go:build irclounge
package main

import (
	"flag"

	"github.com/envsh/fedlet/fbprotocols/irclounge"
)

var ircloungeServer, ircloungeAuth, ircloungeJoin, ircloungeNetwork string

var _ = RegisterProtocol("irclounge", ProtocolCapacities{CanSend: true, CanReceive: true}, func() ProtocolStatus {
	return ProtocolStatus{
		Running:        irclounge.IsRunning(),
		LastErrs:       irclounge.LastErrs(),
		ConnectedSince: irclounge.ConnectedSince(),
		ReconnTimes:    irclounge.ReconnTimes(),
	}
})

func init() {
	irclounge.SetPublishInfo(func(data []byte) error {
		return publish(channel_name, data)
	})
	RegisterSender(TypeLounge, irclounge.Send)
	flag.StringVar(&ircloungeServer, "irclounge", "http://localhost:9000", "The Lounge server URL")
	flag.StringVar(&ircloungeAuth, "irclounge-auth", "", "Lounge user:password (omit for public mode)")
	flag.StringVar(&ircloungeJoin, "irclounge-join", "", "Comma-separated channels to join")
	flag.StringVar(&ircloungeNetwork, "irclounge-network", "", "JSON IRC network config (default: Libera.Chat 6697 TLS)")
	starters = append(starters, func() {
		irclounge.Start(ircloungeServer, ircloungeAuth, ircloungeJoin, ircloungeNetwork)
	})
}
