//go:build irccloud

package main

import (
	"encoding/json"
	"flag"
	"strings"

	"github.com/envsh/fedlet/fbprotocols/irccloud"
)

var ircInfo, ircJoin string

var ircStatusFn = func() ProtocolStatus {
	return ProtocolStatus{
		Running:        irccloud.IsRunning(),
		LastErrs:       irccloud.LastErrs(),
		ConnectedSince: irccloud.ConnectedSince(),
		ReconnTimes:    irccloud.ReconnTimes(),
	}
}

var _ = RegisterProtocol(&ProtocolInfo{
	Name:       "irccloud",
	Ctypes:     []string{TypeIRCCloud},
	Capacities: ProtocolCapacities{CanSend: true, CanReceive: true},
	SendFn:     irccloud.Send,
	StartFn: func() {
		info := ircInfo
		if ircJoin != "" {
			var cfg irccloud.AppConfig
			if err := json.Unmarshal([]byte(ircInfo), &cfg); err != nil {
				if parts := strings.SplitN(ircInfo, ":", 2); len(parts) == 2 {
					cfg.Email = parts[0]
					cfg.Password = parts[1]
				}
			}
			if len(cfg.Servers) == 0 {
				cfg.Channels = splitTrim(ircJoin, ",")
			}
			if b, err := json.Marshal(cfg); err == nil {
				info = string(b)
			}
		}
		irccloud.SetPublishInfo(func(v any) error {
			return publish(channel_name, v)
		})
		irccloud.Start(info)
	},
	statusFn: ircStatusFn,
})

func init() {
	flag.StringVar(&ircInfo, "irc", "", "IRCCloud email:password")
	flag.StringVar(&ircJoin, "irc-join", "#nixos,#firefox,#javascript", "comma-separated IRC channels to join on connect")
}

func splitTrim(s, sep string) []string {
	var out []string
	for _, v := range strings.Split(s, sep) {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
