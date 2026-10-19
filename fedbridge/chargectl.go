// //go:build chargectl

package main

import (
	"flag"
	"fmt"
	"log"
	"strconv"

	"github.com/envsh/fedlet/fbprotocols/chargectl"
)

// chargectl is a purely local backend: it neither publishes nor receives. It is
// registered so main.go calls StartFn and so its status shows up alongside the
// other protocols. -batctl (default true) enables the whole backend; when off it
// releases the charge ceiling and stops the loop. The charging window defaults
// to chargectl.DefaultPauseAt / DefaultResumeAt and is exposed through the
// runtime settings chargectl-pause-at / chargectl-resume-at (env
// FEDLET_CHARGECTL_PAUSE_AT / _RESUME_AT, config.json and the web config page).
var batctl bool

// batctlOn reads the effective value live, so a web PATCH takes effect at once.
var batctlOn = makeMscfgGetter("batctl", true)

// applyBatctl is the hot reload hook: start the loop when enabled, otherwise
// release the hardware ceiling and stop.
func applyBatctl(vals map[string]string) error {
	on := true
	if s := vals["batctl"]; s != "" {
		if v, err := strconv.ParseBool(s); err == nil {
			on = v
		}
	}
	if on {
		chargectl.Start()
		return nil
	}
	chargectl.Release()
	chargectl.Stop()
	return nil
}

func init() {
	flag.BoolVar(&batctl, "batctl", true, "enable local battery charge control")
	registerFlagApply("batctl", applyBatctl)
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
	WebLabel:   "Battery charge control",
	WebSettings: []WebSetting{
		{Flag: "batctl", Key: "enable"},
		{Flag: "chargectl-pause-at", Key: "pause_at"},
		{Flag: "chargectl-resume-at", Key: "resume_at"},
	},
	StartFn: func() {
		if !batctlOn() {
			log.Printf("chargectl: 已由 -batctl=false 关闭")
			return
		}
		chargectl.Start()
	},
	StopFn: chargectl.Stop,
	statusFn: func() ProtocolStatus {
		if !batctlOn() {
			return ProtocolStatus{Running: false, AuthStatus: "disabled"}
		}
		return ProtocolStatus{
			Running:        chargectl.IsRunning(),
			AuthStatus:     chargectl.AuthStatus(),
			LastErrs:       chargectl.LastErrs(),
			ConnectedSince: chargectl.ConnectedSince(),
		}
	},
})
