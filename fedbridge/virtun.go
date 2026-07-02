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
	tunov         tun.Device
	configuredIPs sync.Map
)

/*
sudo setcap cap_net_admin+eip main
*/

func findAvailableUTUN() string {
	out, err := exec.Command("/sbin/ifconfig", "-a").CombinedOutput()
	if err != nil {
		return "utun3"
	}
	used := make(map[int]bool)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "utun") {
			continue
		}
		name := strings.SplitN(line, ":", 2)[0]
		name = strings.TrimPrefix(name, "utun")
		num, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		used[num] = true
	}
	for i := 0; i < 256; i++ {
		if !used[i] {
			return fmt.Sprintf("utun%d", i)
		}
	}
	return "utun3"
}

func setupPFRules() {
	if runtime.GOOS != "darwin" {
		return
	}
	ifname, err := tunov.Name()
	if err != nil {
		log.Printf("pf: get tun name: %v", err)
		return
	}
	out, err := exec.Command("./pf-darwin.sh", "setup", ifname, vlanpfx).CombinedOutput()
	if err != nil {
		log.Printf("pf: setup: %v\n%s", err, string(out))
		return
	}
	log.Printf("pf: loaded rules for %s0/24 via %s", vlanpfx, ifname)
}

func cleanupPFRules() {
	if runtime.GOOS != "darwin" {
		return
	}
	out, err := exec.Command("./pf-darwin.sh", "cleanup").CombinedOutput()
	if err != nil {
		log.Printf("pf: cleanup: %v\n%s", err, string(out))
	}
}

// FUTURE: DIOCNATLOOK — recover original dst IP/port from pf state table
// After accept(), query pf NAT table via /dev/pf ioctl:
//
//   import "golang.org/x/sys/unix"
//
//   func lookupOriginalDst(conn net.Conn) (net.IP, int, error) {
//        // 1. get conn's fd via unix.Getsockname or syscall.Getpeername equivalent
//        // 2. open /dev/pf, issue DIOCNATLOOK (IOWR('D',31, struct pf_natlook))
//        // 3. populate lookup: .saddr=clientIP .sport=clientPort .dport=localPort .proto=IPPROTO_TCP .direction=PF_IN
//        // 4. ioctl returns .rdaddr=originalDstIP .rdport=originalDstPort
//        // Reference: sys/net/pfvar.h on macOS
//        // Used by: Squid, Tailscale (tun_macos.go)
//   }
//
//   struct pf_natlook (from Apple's pfvar.h):
//       struct pf_addr saddr;     // source address of packet
//       struct pf_addr daddr;     // destination address (127.0.0.1 after rdr)
//       struct pf_addr rdaddr;    // original destination (10.0.0.97 before rdr)
//       u_int16_t sport;          // source port
//       u_int16_t dport;          // destination port (after rdr)
//       u_int16_t rdport;         // original destination port
//       u_int8_t  proto;          // IPPROTO_TCP / IPPROTO_UDP
//       u_int8_t  direction;      // PF_IN / PF_OUT
//       sa_family_t af;           // AF_INET
//   }

func initVirTun(keyFile string) error {
	t, err := tun.CreateTUN(findAvailableUTUN(), 1900)
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

	if runtime.GOOS == "darwin" {
		setupPFRules()
	}

	return nil
}

func hasExistingIP(ifname string) bool {
	out, err := exec.Command("/sbin/ifconfig", ifname).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "inet ")
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
		args := []string{ifname, "inet", ip, ip, "netmask", "255.255.255.0"}
		if hasExistingIP(ifname) {
			args = append(args, "alias")
		}
		out, err := exec.Command("/sbin/ifconfig", args...).CombinedOutput()
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
