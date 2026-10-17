// //go:build chargectl

package main

import (
	"fmt"
	"strconv"

	"github.com/envsh/fedlet/fbprotocols/chargectl"
)

// chargectl is a purely local backend: it neither publishes nor receives. It is
// registered so main.go calls StartFn and so its status shows up alongside the
// other protocols. The charging window defaults to chargectl.DefaultPauseAt /
// DefaultResumeAt and is exposed through the flag-less runtime settings
// chargectl-pause-at / chargectl-resume-at (env FEDLET_CHARGECTL_PAUSE_AT /
// _RESUME_AT, config.json and the web config page); there is no CLI flag.

func init() {
	registerMscfgExtra("chargectl-pause-at", mscfgExtraSpec{
		dft:    strconv.Itoa(chargectl.DefaultPauseAt),
		typ:    "number",
		verify: checkChargectlWindow,
		apply:  applyChargectlWindow,
	})
	registerMscfgExtra("chargectl-resume-at", mscfgExtraSpec{
		dft:    strconv.Itoa(chargectl.DefaultResumeAt),
		typ:    "number",
		verify: checkChargectlWindow,
		apply:  applyChargectlWindow,
	})
}

func parsePct(name string, vals map[string]string, dft int) (int, error) {
	s := vals[name]
	if s == "" {
		return dft, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s 需为整数(当前 %q)", name, s)
	}
	return v, nil
}

// checkChargectlWindow 对 pause/resume 成对做范围与回滞校验。
func checkChargectlWindow(vals map[string]string) error {
	pause, err := parsePct("chargectl-pause-at", vals, chargectl.DefaultPauseAt)
	if err != nil {
		return err
	}
	resume, err := parsePct("chargectl-resume-at", vals, chargectl.DefaultResumeAt)
	if err != nil {
		return err
	}
	return chargectl.ValidateWindow(pause, resume)
}

func applyChargectlWindow(vals map[string]string) error {
	pause := chargectl.DefaultPauseAt
	if v, err := strconv.Atoi(vals["chargectl-pause-at"]); err == nil {
		pause = v
	}
	resume := chargectl.DefaultResumeAt
	if v, err := strconv.Atoi(vals["chargectl-resume-at"]); err == nil {
		resume = v
	}
	return chargectl.SetThresholds(pause, resume)
}

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
