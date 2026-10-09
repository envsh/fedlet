//go:build sysinfo

package main

import (
	"flag"
	"time"

	"github.com/envsh/fedlet/fbprotocols/sysinfo"
)

var sysinfoInterval time.Duration

var _ = RegisterProtocol(&ProtocolInfo{
	Name:       "sysinfo",
	Ctypes:     []string{"sysinfo"},
	Capacities: ProtocolCapacities{CanReceive: true},
	StartFn: func() {
		sysinfo.SetPublishInfo(func(v any) error {
			return publish("sysinfo", channel_name, v)
		})
		sysinfo.Start(sysinfoInterval)
	},
	StopFn: sysinfo.Stop,
	statusFn: func() ProtocolStatus {
		return ProtocolStatus{
			Running:        sysinfo.IsRunning(),
			AuthStatus:     sysinfo.AuthStatus(),
			LastErrs:       sysinfo.LastErrs(),
			ConnectedSince: sysinfo.ConnectedSince(),
		}
	},
})

func init() {
	flag.DurationVar(&sysinfoInterval, "sysinfo-interval", sysinfo.DefaultInterval,
		"sysinfo sampling interval (0=123s default)")
}
