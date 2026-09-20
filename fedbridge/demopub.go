package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/envsh/libp2px/fbvirtun"
)

// virtIP 返回本节点虚拟 LAN IPv4:优先取已就绪的 LocalPeerIP,
// 否则由 localPeerID 按 vlanpfx + hostpart 现算;都不可得则空串。
func virtIP() string {
	if ip := fbvirtun.LocalPeerIP; ip != "" {
		return ip
	}
	if localPeerID == "" {
		return ""
	}
	return vlanpfx + strconv.Itoa(fbvirtun.StringToHostPart(localPeerID))
}

// peerID7 返回本节点 peer id 的后 7 位(localPeerID 为空时回退 currentPeerID)。
func peerID7() string {
	pid := localPeerID
	if pid == "" {
		pid = currentPeerID
	}
	if len(pid) <= 7 {
		return pid
	}
	return pid[len(pid)-7:]
}

// would block, call with `go`
func poll_demopub() {
	// var channel_name = "v2ex"

	for i := 0; ; i++ {
		time.Sleep(35 * time.Second) // many nodes pub xN msgs
		scc := fmt.Sprintf(`{"vvv": "ddddddd %v", "virtip4": "%s", "peerid7": "%s"}`, i, virtIP(), peerID7())
		err := publish("demopub", channel_name, json.RawMessage(scc))
		if err != nil {
			log.Println(i, err)
		}
	}
}
