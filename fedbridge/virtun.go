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

	go tunFixChecksums()

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
		ifname, err := tunov.Name()
		if err != nil {
			return fmt.Errorf("add ip: get tun name: %w", err)
		}
		link, err := netlink.LinkByName(ifname)
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

// tunFixChecksums completes L4 checksums for self-addressed TCP/UDP packets
// on the macOS utun hairpin path.
//
// macOS routes self-addressed traffic as loopback, deferring the transport TX
// checksum — but the point-to-point utun egresses the packet with only the
// pseudo-header partial checksum. Recomputing the full checksum before
// re-injection is the confirmed fix.
//
// References:
//
//	fips PR #117 / commit 225fab2 — "fix(tun): complete L4 checksum on
//	hairpinned self-traffic (macOS)".  Confirmed the exact symptom: SYN/SYN-ACK
//	get through (MSS clamping rewrites checksum), bare ACK/data/FIN are silently
//	dropped.  Fix: recompute_transport_checksum() before re-injecting.
//	https://github.com/jmcorgan/fips/pull/117
//
//	StackOverflow #76876059 — "Why packets seem not relayed to target
//	applications using TUN interface?"  "On utun interfaces, checksum validation
//	is absolutely required for avoiding that kernel discard packets."
//	https://stackoverflow.com/questions/76876059
//
//	WireGuard-go tun_darwin.go — macOS utun reads/writes with 4-byte AF_INET
//	prefix.  Read strips it, Write prepends it.  Used as reference for the
//	buffer layout with offset=4.
//	https://git.zx2c4.com/wireguard-go/tree/tun/tun_darwin.go
func tunFixChecksums() {
	if tunov == nil {
		return
	}
	buf := make([]byte, 2004)
	for {
		n, err := tunov.Read(buf, 4)
		if err != nil || n < 20 {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		pkt := buf[4 : 4+n]

		if pkt[0]>>4 != 4 {
			tunov.Write(buf[:4+n], 4)
			continue
		}
		ipHdrLen := (pkt[0] & 0x0F) * 4
		if len(pkt) < int(ipHdrLen) {
			continue
		}
		proto := pkt[9]

		pkt[10], pkt[11] = 0, 0
		csum := onesComplementSum(pkt[:ipHdrLen])
		pkt[10] = byte(csum >> 8)
		pkt[11] = byte(csum & 0xFF)

		switch proto {
		case 6:
			off := int(ipHdrLen)
			if len(pkt) < off+20 {
				break
			}
			totalLen := uint16(len(pkt) - off)
			pkt[off+16], pkt[off+17] = 0, 0
			psum := pseudoChecksum(pkt[12:16], pkt[16:20], 6, totalLen)
			csum = onesComplementSumFold(pkt[off:], psum)
			pkt[off+16] = byte(csum >> 8)
			pkt[off+17] = byte(csum & 0xFF)

		case 17:
			off := int(ipHdrLen)
			if len(pkt) < off+8 {
				break
			}
			totalLen := uint16(len(pkt) - off)
			pkt[off+6], pkt[off+7] = 0, 0
			psum := pseudoChecksum(pkt[12:16], pkt[16:20], 17, totalLen)
			csum = onesComplementSumFold(pkt[off:], psum)
			pkt[off+6] = byte(csum >> 8)
			pkt[off+7] = byte(csum & 0xFF)
		}

		tunov.Write(buf[:4+n], 4)
	}
}

// onesComplementSum computes an RFC 1071 one's complement Internet checksum
// over data.  Reference: WireGuard-go tun/checksum.go checksum().
func onesComplementSum(data []byte) uint16 {
	var sum uint32
	for i := 0; i < len(data)-1; i += 2 {
		sum += uint32(data[i])<<8 | uint32(data[i+1])
	}
	if len(data)%2 == 1 {
		sum += uint32(data[len(data)-1]) << 8
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}

// pseudoChecksum returns the RFC 1071 pseudo-header sum for L4 checksum
// computation.  Reference: WireGuard-go tun/checksum.go
// pseudoHeaderChecksumNoFold().
func pseudoChecksum(src, dst []byte, proto byte, totalLen uint16) uint32 {
	return (uint32(src[0])<<8 | uint32(src[1])) +
		(uint32(src[2])<<8 | uint32(src[3])) +
		(uint32(dst[0])<<8 | uint32(dst[1])) +
		(uint32(dst[2])<<8 | uint32(dst[3])) +
		uint32(proto) + uint32(totalLen)
}

// onesComplementSumFold continues an RFC 1071 checksum computation with an
// existing accumulator (from pseudoChecksum).  Reference: WireGuard-go
// tun/checksum.go checksum().
func onesComplementSumFold(data []byte, initial uint32) uint16 {
	var sum uint32 = initial
	for i := 0; i < len(data)-1; i += 2 {
		sum += uint32(data[i])<<8 | uint32(data[i+1])
	}
	if len(data)%2 == 1 {
		sum += uint32(data[len(data)-1]) << 8
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}
