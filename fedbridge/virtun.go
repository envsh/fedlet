package main

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc64"
	"log"
	"net"
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

/*
   对于对端无tun设备的情况，采用隐式添加proxy的方式，需要识别协议的功能

   那就假设对端都没有tun设备，然后走同一套实现逻辑。有tun设备的端点能够随意发起连接，没有tun设备的端点，只能做接收端服务端，发起请求需要代码实现。

https://github.com/tun2proxy/tun2proxy 似乎是针对特定场景的实现，不太通用，需要部署参数，用起来可能不通用了。。。

*/

var (
	tunov         tun.Device
	configuredIPs sync.Map
	tunMTU        int
	tunOffset     int
	peerIPMap     map[string]string
	peerIPMu      sync.RWMutex
	tunBufSize    int
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
		tunMTU = 1900
		tunOffset = 0
		if runtime.GOOS == "darwin" {
			// macOS utun requires a 4-byte AF_INET/AF_INET6 prefix before the
			// IP packet.  The tun device reads/writes with this offset so the
			// 4-byte family field sits at buf[0:4] and the IP packet starts at
			// buf[4:].
			// Reference: wireguard-go/tun/tun_darwin.go — NativeTUN Read/Write.
			tunOffset = 4
		}
		if runtime.GOOS == "linux" {
			// wireguard-go/tun.CreateTUN always sets IFF_VNET_HDR
			// (tun_linux.go:566).  When vnetHdr is active, Write calls
			// handleGRO() (offload_linux.go:865) which requires
			// offset >= virtioNetHdrLen (12 bytes = sizeof(virtioNetHdr)),
			// otherwise it returns "invalid offset".
			// Read with vnetHdr uses an internal readBuff and copies the IP
			// packet to bufs[0][offset:]; offset=12 is safe for both directions.
			tunOffset = 12
		}
		tunBufSize = tunMTU + tunOffset + 4
		ifname, _ := tunov.Name()
		log.Println("tundev created", ifname)
	}

	go tunReadLoop()

	go func() {
		if tunov == nil {
			return
		}
		for {
			time.Sleep(2 * time.Second)
			for _, p := range getPeerList() {
				ip := vlanpfx + strconv.Itoa(stringToHostPart(p.ID))
				peerIPMu.Lock()
				if peerIPMap == nil {
					peerIPMap = make(map[string]string)
				}
				peerIPMap[ip] = p.ID
				peerIPMu.Unlock()
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

	go func() {
		for {
			time.Sleep(30 * time.Second)
			tcpCleanup()
			udpCleanup()
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
	case "android":
		ifname, err := tunov.Name()
		if err != nil {
			return fmt.Errorf("add ip: get tun name: %w", err)
		}
		out, err := exec.Command("ip", "addr", "add", ip+"/24", "dev", ifname).CombinedOutput()
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				log.Printf("virtun: 'ip' command not found — install iproute2 (Termux: pkg install iproute2) or run with root")
				return nil
			}
			return fmt.Errorf("add ip: %s", strings.TrimSpace(string(out)))
		}
		out, err = exec.Command("ip", "link", "set", ifname, "up").CombinedOutput()
		if err != nil {
			return fmt.Errorf("add ip: link up: %s", strings.TrimSpace(string(out)))
		}
		return nil
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
func pseudoChecksum6(src, dst []byte, proto byte, totalLen uint16) uint32 {
	var sum uint32
	for i := 0; i < 16; i += 2 {
		sum += uint32(src[i])<<8 | uint32(src[i+1])
	}
	for i := 0; i < 16; i += 2 {
		sum += uint32(dst[i])<<8 | uint32(dst[i+1])
	}
	var len32 [4]byte
	binary.BigEndian.PutUint32(len32[:], uint32(totalLen))
	sum += uint32(len32[0])<<8 | uint32(len32[1])
	sum += 0
	sum += uint32(proto)
	return sum
}

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

// tunReadLoop handles all TUN I/O — hairpin, forwarding, logging.
//
// macOS utun hairpin: self-addressed traffic via loopback defers transport TX
// checksum, so the utun egress carries only a pseudo-header partial checksum.
// SYN/SYN-ACK survive (MSS clamping rewrites checksum), but ACK/data/FIN are
// silently dropped.  We detect hairpin by dst==localIP and recompute.
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
func tunReadLoop() {
	if tunov == nil {
		return
	}
	for localPeerIP == "" {
		time.Sleep(100 * time.Millisecond)
	}
	localIP := net.ParseIP(localPeerIP)
	buf := make([]byte, tunBufSize)
	bufs := [][]byte{buf}
	sizes := make([]int, 1)

	for {
		_, err := tunov.Read(bufs, sizes, tunOffset)
		n := sizes[0]
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		pkt := buf[tunOffset : tunOffset+n]

		version := pkt[0] >> 4
		if version != 4 && version != 6 {
			log.Printf("tun: unsupported IP version %d len=%d", version, n)
			continue
		}

		var srcIP, dstIP net.IP
		var proto byte
		var ihl int

		switch version {
		case 4:
			if len(pkt) < 20 {
				continue
			}
			srcIP = net.IP(pkt[12:16])
			dstIP = net.IP(pkt[16:20])
			proto = pkt[9]
			ihl = int(pkt[0]&0x0F) * 4
		case 6:
			if len(pkt) < 40 {
				continue
			}
			srcIP = net.IP(pkt[8:24])
			dstIP = net.IP(pkt[24:40])
			proto = pkt[6]
			ihl = 40
		}

		dstStr := dstIP.String()

		switch {
		case dstIP.Equal(localIP):
			if runtime.GOOS == "darwin" && version == 4 && isTCPorUDP(proto) {
				fixIPChecksum(pkt)
				if proto == 6 {
					fixTCPChecksum(pkt, ihl)
				} else {
					fixUDPChecksum(pkt, ihl)
				}
			}
			tunov.Write(bufs[:1], tunOffset)
			log.Printf("tun: hairpin v%d src=%s dst=%s proto=%d len=%d [+]",
				version, srcIP, dstIP, proto, n)

		case func() bool { peerIPMu.RLock(); defer peerIPMu.RUnlock(); _, ok := peerIPMap[dstStr]; return ok }():
			peerIPMu.RLock()
			pid := peerIPMap[dstStr]
			peerIPMu.RUnlock()
			if proto == 6 || proto == 17 {
				sport, dport := parsePorts(pkt, proto, ihl)
				log.Printf("tun: forward v%d src=%s:%d dst=%s:%d proto=%s len=%d → peer=%s [-]",
					version, srcIP, sport, dstIP, dport, protoName(proto), n, pid)
			} else {
				log.Printf("tun: forward v%d src=%s dst=%s proto=%d len=%d → peer=%s [-]",
					version, srcIP, dstIP, proto, n, pid)
			}

		case version == 4 && dstIP.Equal(net.IPv4bcast):
			log.Printf("tun: broadcast v%d src=%s dst=%s proto=%d len=%d [-]",
				version, srcIP, dstIP, proto, n)

		case version == 6 && dstIP.IsMulticast():
			log.Printf("tun: multicast v6 src=%s dst=%s proto=%d len=%d [-]",
				srcIP, dstIP, proto, n)

		case dstIP.IsLoopback():
			log.Printf("tun: loopback v%d src=%s dst=%s proto=%d len=%d [-]",
				version, srcIP, dstIP, proto, n)

		case dstIP.IsLinkLocalUnicast():
			log.Printf("tun: linklocal v%d src=%s dst=%s proto=%d len=%d [-]",
				version, srcIP, dstIP, proto, n)

		default:
			switch proto {
			case 1:
				log.Printf("tun: ICMP v%d src=%s dst=%s type=%s len=%d [-]",
					version, srcIP, dstIP, icmpTypeStr(4, pkt, ihl), n)
			case 6:
				sport, dport := parsePorts(pkt, 6, ihl)
				log.Printf("tun: TCP v%d src=%s:%d dst=%s:%d len=%d [+]",
					version, srcIP, sport, dstIP, dport, n)
				if version == 4 {
					var src, dst [4]byte
					copy(src[:], srcIP.To4())
					copy(dst[:], dstIP.To4())
					handleTCP4(pkt, ihl, src, dst)
				} else {
					var src, dst [16]byte
					copy(src[:], srcIP.To16())
					copy(dst[:], dstIP.To16())
					handleTCP6(pkt, src, dst)
				}
			case 17:
				sport, dport := parsePorts(pkt, 17, ihl)
				log.Printf("tun: UDP v%d src=%s:%d dst=%s:%d len=%d [+]",
					version, srcIP, sport, dstIP, dport, n)
				if version == 4 {
					var src, dst [4]byte
					copy(src[:], srcIP.To4())
					copy(dst[:], dstIP.To4())
					handleUDP4(pkt, ihl, src, dst)
				} else {
					var src, dst [16]byte
					copy(src[:], srcIP.To16())
					copy(dst[:], dstIP.To16())
					handleUDP6(pkt, src, dst)
				}
			case 58:
				log.Printf("tun: ICMPv6 v%d src=%s dst=%s type=%s len=%d [-]",
					version, srcIP, dstIP, icmpTypeStr(6, pkt, ihl), n)
			default:
				log.Printf("tun: PROTO-%d v%d src=%s dst=%s len=%d [-]",
					proto, version, srcIP, dstIP, n)
			}
		}
	}
}

func isTCPorUDP(proto byte) bool { return proto == 6 || proto == 17 }

func protoName(proto byte) string {
	switch proto {
	case 1:
		return "ICMP"
	case 6:
		return "TCP"
	case 17:
		return "UDP"
	default:
		return fmt.Sprintf("UNKN(%d)", proto)
	}
}

func parsePorts(pkt []byte, proto byte, ihl int) (sport, dport uint16) {
	if len(pkt) < ihl+4 {
		return 0, 0
	}
	return binary.BigEndian.Uint16(pkt[ihl:]), binary.BigEndian.Uint16(pkt[ihl+2:])
}

func fixIPChecksum(pkt []byte) {
	ihl := int(pkt[0]&0x0F) * 4
	pkt[10], pkt[11] = 0, 0
	csum := onesComplementSum(pkt[:ihl])
	pkt[10] = byte(csum >> 8)
	pkt[11] = byte(csum & 0xFF)
}

func fixTCPChecksum(pkt []byte, ihl int) {
	off := ihl
	if len(pkt) < off+20 {
		return
	}
	totalLen := uint16(len(pkt) - off)
	pkt[off+16], pkt[off+17] = 0, 0
	psum := pseudoChecksum(pkt[12:16], pkt[16:20], 6, totalLen)
	csum := onesComplementSumFold(pkt[off:], psum)
	pkt[off+16] = byte(csum >> 8)
	pkt[off+17] = byte(csum & 0xFF)
}

func fixUDPChecksum(pkt []byte, ihl int) {
	off := ihl
	if len(pkt) < off+8 {
		return
	}
	totalLen := uint16(len(pkt) - off)
	pkt[off+6], pkt[off+7] = 0, 0
	psum := pseudoChecksum(pkt[12:16], pkt[16:20], 17, totalLen)
	csum := onesComplementSumFold(pkt[off:], psum)
	pkt[off+6] = byte(csum >> 8)
	pkt[off+7] = byte(csum & 0xFF)
}

func icmpTypeStr(version int, pkt []byte, ihl int) string {
	if version == 4 {
		if len(pkt) < ihl+1 {
			return ""
		}
		t := pkt[ihl]
		switch t {
		case 0:
			return "EchoReply"
		case 3:
			return "DestUnreach"
		case 8:
			return "Echo"
		case 11:
			return "TimeExceed"
		default:
			return fmt.Sprintf("ICMP-%d", t)
		}
	}
	if version == 6 {
		if len(pkt) < ihl+1 {
			return ""
		}
		t := pkt[ihl]
		switch t {
		case 1:
			return "DestUnreach"
		case 128:
			return "Echo"
		case 129:
			return "EchoReply"
		case 130:
			return "MLD-Query"
		case 131:
			return "MLD-Report-v1"
		case 132:
			return "MLD-Done"
		case 133:
			return "RouterSolicit"
		case 134:
			return "RouterAdv"
		case 135:
			return "NeighborSolicit"
		case 136:
			return "NeighborAdv"
		case 143:
			return "MLD-Report-v2"
		default:
			return fmt.Sprintf("ICMPv6-%d", t)
		}
	}
	return ""
}
