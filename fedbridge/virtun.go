package main

import (
	"bufio"
	"encoding/hex"
	"hash/crc64"
	"log"
	"os"
	"strconv"
	"strings"

	"golang.zx2c4.com/wireguard/tun"
)

var tunov tun.Device

/*
sudo setcap cap_net_admin+eip main
*/

func initVirTun(keyFile string) {
	hostPart := computeHostPart(keyFile)
	ip := vlanpfx + strconv.Itoa(hostPart)
	log.Printf("virtun: computed IP: %s", ip)

	t, err := tun.CreateTUN("fedlet", 1900)
	if err != nil {
		log.Println(err, "recheck modprobe tun")
	} else {
		tunov = t
		log.Println("tundev created", "fedlet")
	}
}

func computeHostPart(keyFile string) int {
	// FIXME: replace with p2put.SeedHex() when available
	f, err := os.Open(keyFile)
	if err != nil {
		return 2
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "seed=") {
			s := strings.TrimSpace(line[5:])
			data, err := hex.DecodeString(s)
			if err != nil || len(data) == 0 {
				break
			}
			tbl := crc64.MakeTable(crc64.ECMA)
			h := crc64.Checksum(data, tbl)
			return int(h%253) + 2
		}
	}
	return 2
}
