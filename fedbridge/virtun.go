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

	"github.com/vishvananda/netlink"
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
		log.Println(err, "recheck modprobe tun or root/cap_net_admin")
		return
	} else {
		tunov = t
		log.Println("tundev created", "fedlet")
	}

	if err := setupSeedVirtIP(ip); err != nil {
		log.Printf("virtun: %v", err)
	} else {
		log.Printf("virtun: %s configured and up", ip)
	}
}

func setupSeedVirtIP(ip string) error {
	switch runtime.GOOS {
	case "linux":
		link, err := netlink.LinkByName("fedlet")
		if err != nil {
			return fmt.Errorf("setup seed virt IP: link fedlet: %w", err)
		}
		addr, err := netlink.ParseAddr(ip + "/24")
		if err != nil {
			return fmt.Errorf("setup seed virt IP: parse addr: %w", err)
		}
		if err := netlink.AddrAdd(link, addr); err != nil {
			return fmt.Errorf("setup seed virt IP: addr add: %w", err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			return fmt.Errorf("setup seed virt IP: link set up: %w", err)
		}
	case "darwin":
		out, err := exec.Command("ifconfig", "fedlet", "inet", ip, ip, "netmask", "255.255.255.0", "up").CombinedOutput()
		if err != nil {
			return fmt.Errorf("setup seed virt IP: ifconfig: %s", strings.TrimSpace(string(out)))
		}
	case "windows":
		out, err := exec.Command("netsh", "interface", "ip", "set", "address",
			"name=fedlet", "source=static", "addr="+ip, "mask=255.255.255.0").CombinedOutput()
		if err != nil {
			return fmt.Errorf("setup seed virt IP: netsh: %s", strings.TrimSpace(string(out)))
		}
	default:
		return fmt.Errorf("setup seed virt IP: unsupported platform: %s", runtime.GOOS)
	}
	return nil
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
