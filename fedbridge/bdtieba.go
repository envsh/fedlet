//go:build bdtieba

package main

import (
	"flag"
	"strings"
	"time"

	"github.com/envsh/fedlet/fbprotocols/bdtieba"
)

var (
	tiebaKws      string
	tiebaInterval time.Duration
)

// tiebaSplit splits a comma-separated forum list, returning nil when empty so
// that bdtieba.Start falls back to its built-in default forums.
func tiebaSplit(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, v := range strings.Split(s, ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

var _ = RegisterProtocol(&ProtocolInfo{
	Name:       "bdtieba",
	Ctypes:     []string{"bdtieba"},
	Capacities: ProtocolCapacities{CanReceive: true},
	StartFn: func() {
		bdtieba.SetPublishInfo(func(v any) error {
			return publish("bdtieba", channel_name, v)
		})
		bdtieba.Start(tiebaSplit(tiebaKws), tiebaInterval)
	},
	statusFn: func() ProtocolStatus {
		return ProtocolStatus{
			Running:        bdtieba.IsRunning(),
			LastErrs:       bdtieba.LastErrs(),
			ConnectedSince: bdtieba.ConnectedSince(),
		}
	},
})

func init() {
	flag.StringVar(&tiebaKws, "tiebakw", "", "comma-separated Tieba forum names (empty=default)")
	flag.DurationVar(&tiebaInterval, "tiebainterval", 600*time.Second, "poll interval (0=default)")
}