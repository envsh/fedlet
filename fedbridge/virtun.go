package main

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"hash/crc64"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/tun"
)

var (
	tunov        tun.Device
	configuredIPs sync.Map
)

/*
sudo setcap cap_net_admin+eip main
*/

func initVirTun(keyFile string) error {
	t, err := tun.CreateTUN("fedlet", 1500)
	if err != nil {
		log.Println(err, "recheck modprobe tun or root/cap_net_admin")
		log.Println("    On MacOS, sudo chown root:wheel main && sudo chmod u+s main")
		return fmt.Errorf("create tun: %w", err)
	} else {
		tunov = t
		ifname, _ := tunov.Name()
		log.Println("tundev created", ifname)
	}

	go func() {
		if tunov == nil {
			return
		}
		for {
			time.Sleep(2 * time.Second)
			for _, p := range getPeerList() {
				ip := vlanpfx + strconv.Itoa(stringToHostPart(p.ID))
				if _, ok := configuredIPs.Load(ip); ok {
					continue
				}
				if err := addIPToTun(ip); err != nil {
					log.Printf("virtun: add ip %s: %v", ip, err)
				} else {
					configuredIPs.Store(ip, true)
					log.Printf("virtun: added peer ip %s", ip)
				}
			}
		}
	}()

	return nil
}

func addIPToTun(ip string) error {
	switch runtime.GOOS {
	case "linux":
		link, err := netlink.LinkByName("fedlet")
		if err != nil {
			return err
		}
		if err := netlink.LinkSetUp(link); err != nil {
			return err
		}
		if err := netlink.LinkSetTxQLen(link, 1000); err != nil {
			return err
		}
		addr, err := netlink.ParseAddr(ip + "/24")
		if err != nil {
			return err
		}
		return netlink.AddrAdd(link, addr)
	case "darwin":
		ifname, err := tunov.Name()
		if err != nil {
			return fmt.Errorf("add ip: get tun name: %w", err)
		}
		out, err := exec.Command("ifconfig", ifname, "inet", ip, ip, "netmask", "255.255.255.0", "alias").CombinedOutput()
		if err != nil {
			return fmt.Errorf("add ip: %s", strings.TrimSpace(string(out)))
		}
		return nil
	case "windows":
		out, err := exec.Command("netsh", "interface", "ip", "add", "address",
			"name=fedlet", "addr="+ip, "mask=255.255.255.0").CombinedOutput()
		if err != nil {
			return fmt.Errorf("add ip: %s", strings.TrimSpace(string(out)))
		}
	}
	return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
}

func setupSeedVirtIP(ip string) error {
	return addIPToTun(ip)
}

func stringToHostPart(s string) int {
	tbl := crc64.MakeTable(crc64.ECMA)
	h := crc64.Checksum([]byte(s), tbl)
	return int(h%253) + 2
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
