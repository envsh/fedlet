//go:build xhs

package main

import (
	"time"

	"github.com/envsh/fedlet/fbprotocols/xhs"
)

var (
	xhsHot          = true
	xhsNotify       = true
	// xhsInterval is the hot-board round cadence: exactly ONE uapis type is
	// fetched per round, rotating through the On entries of hotBoards
	// (10 enabled × 61s ≈ 10m10s per board; uapis refreshes data every ~5min,
	// request rate 1/min, well under their 40/min fair-use guidance).
	xhsInterval     = 61 * time.Second
	xhsNotifyIntval = 60 * time.Second
)

var _ = RegisterProtocol(&ProtocolInfo{
	Name:       "xhs",
	Ctypes:     []string{"xhs"},
	Capacities: ProtocolCapacities{CanReceive: true},
	StartFn: func() {
		xhs.SetPublishInfo(func(v any) error {
			return publish("xhs", channel_name, v)
		})
		xhs.Start(xhsHot, xhsNotify, xhsInterval, xhsNotifyIntval)
	},
	statusFn: func() ProtocolStatus {
		return ProtocolStatus{
			Running:        xhs.IsRunning(),
			AuthStatus:     xhs.AuthStatus(),
			LastErrs:       xhs.LastErrs(),
			ConnectedSince: xhs.ConnectedSince(),
		}
	},
})