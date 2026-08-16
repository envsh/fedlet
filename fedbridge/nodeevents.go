package main

import (
	"log"
	"strconv"
	"sync"

	"github.com/envsh/libp2px/fbvirtun"
)

type nodeEventBus struct {
	mu       sync.Mutex
	handlers map[string][]func(any)
}

var nodeBus = &nodeEventBus{
	handlers: make(map[string][]func(any)),
}

func (b *nodeEventBus) On(typ string, fn func(any)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[typ] = append(b.handlers[typ], fn)
}

func (b *nodeEventBus) Emit(typ string, v any) {
	b.mu.Lock()
	fns := append([]func(any){}, b.handlers[typ]...)
	b.mu.Unlock()
	for _, fn := range fns {
		go func(f func(any)) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("nodebus: handler %s panic: %v", typ, r)
				}
			}()
			f(v)
		}(fn)
	}
}

// onBootReady 原 bootready 检测循环的内联逻辑,抽为事件回调。
func onBootReady(v any) {
	peerID, _ := v.(string)
	if peerID == "" || fbvirtun.Tunov == nil {
		return
	}
	localPeerID = peerID
	hostPart := fbvirtun.StringToHostPart(peerID)
	ip := vlanpfx + strconv.Itoa(hostPart)
	fbvirtun.LocalPeerIP = ip
	log.Printf("virtun: computed IP from peer_id: %s", ip)
	if err := fbvirtun.SetupSeedVirtIP(ip); err != nil {
		log.Printf("virtun: %v", err)
	} else {
		log.Printf("virtun: %s configured and up from peer_id", ip)
	}
	if fbvirtun.IPv6Available() {
		for _, pfx := range ipv6Prefixes {
			addr := pfx + strconv.Itoa(hostPart)
			if fbvirtun.LocalPeerIPv6 == "" {
				fbvirtun.LocalPeerIPv6 = addr
			}
			if err := fbvirtun.SetupSeedVirtIP(addr); err != nil {
				log.Printf("virtun: %s: %v", addr, err)
			} else {
				log.Printf("virtun: %s configured and up", addr)
			}
		}
	} else {
		log.Printf("virtun: IPv6 not available (kernel disabled), skipping")
	}
}

// onTargetPeer 只做动作;检测与 log 保留在 waitPeerCome。
func onTargetPeer(v any) {
	pid, _ := v.(string)
	if pid == "" {
		return
	}
	if driftsrv != nil {
		driftsrv.SwitchPeer(pid)
	}
	currentPeerID = pid
}
