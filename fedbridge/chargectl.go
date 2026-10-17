// //go:build chargectl

package main

import (
	"github.com/envsh/fedlet/fbprotocols/chargectl"
)

// chargectl is a purely local backend: it neither publishes nor receives. It is
// registered so main.go calls StartFn and so its status shows up alongside the
// other protocols. The charging policy is hardcoded in the package; there is no
// flag and no config file.
var _ = RegisterProtocol(&ProtocolInfo{
	Name:       "chargectl",
	Ctypes:     []string{"chargectl"},
	Capacities: ProtocolCapacities{},
	StartFn:    chargectl.Start,
	StopFn:     chargectl.Stop,
	statusFn: func() ProtocolStatus {
		return ProtocolStatus{
			Running:        chargectl.IsRunning(),
			AuthStatus:     chargectl.AuthStatus(),
			LastErrs:       chargectl.LastErrs(),
			ConnectedSince: chargectl.ConnectedSince(),
		}
	},
})
