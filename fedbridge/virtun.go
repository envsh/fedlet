package main

import (
	"log"

	"golang.zx2c4.com/wireguard/tun"
)

var tunov tun.Device

/*
sudo setcap cap_net_admin+eip main
*/

func initVirTun() {
	t, err := tun.CreateTUN("fedlet", 1900)
	if err != nil {
		log.Println(err, "recheck modprobe tun")
	} else {
		tunov = t
		log.Println("tundev created", "fedlet")
	}
}
