
package main

import (
    "fmt"
    "net"
	"io"
	"log"
    // "net/http"
	"strconv"
    "time"

    // "github.com/envsh/libp2px/p2put"
    "github.com/envsh/libp2px/softun"
	// _ "github.com/envsh/libp2px/softun"
)

func runSoftunMain(locid string) {
    // ── 0. 启动 libp2px（p2put 内部已有真实网络层）──
    // p2put.Start() ... （按你的既有启动流程）
    // localPeerID := p2put.SomeLocalID() // 占位：取本节点 peer ID 的地方
	if locid == "" {
		log.Panicln("Empty locid")
	}

    // ── 1. 初始化 softun：用户态 tun 栈 + 注册为 iptunnel 的 Sink ──
    if err := softun.InitSoftTun(locid); err != nil {
        panic(err)
    }

    // ── 2. 开启 loportmap（默认关）──
    // 开启后：本机 127.0.0.1:port 的服务，可从虚拟 LAN 以 虚拟IP:port 访问
    softun.EnableLoPortMap() // DisableLoPortMap() 可关闭并释放端口

    myIP := softun.LocalIP() // 例如 10.0.0.17

    // ── 3. 本地应用接入虚拟 LAN ──
    // 3a. 虚拟 LAN 监听（服务端）
    ln, err := softun.Device().Listen("tcp", myIP+":8080")
    if err != nil { panic(err) }
	log.Println("softun listen on:", ln.Addr())

    softunAcceptLoop(ln)
	log.Println("softun mainproc done")
}

func runSoftunPhyport(port int) {
	lsner, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Panicln(err)
	}
	for {
		c, err := lsner.Accept()
		if err != nil {
			break
		}
		peerid := currentPeerID
		if peerid == "" {
			log.Println("currentPeerID not set")
			c.Close()
			continue
		}
		peerIP := vlanpfx + strconv.Itoa(softun.StringToHostPart(peerid))
		go func(c net.Conn) {
			defer c.Close()
			conn, err := softun.Device().Dial("tcp", peerIP+":9229")
			if err != nil {
				log.Printf("softun phyport: dial %s:9229: %v", peerIP, err)
				return
			}
			defer conn.Close()
			go io.Copy(conn, c)
			io.Copy(c, conn)
		}(c)
	}
	log.Println("softun phyport done", port)
}

func testSoftunPeer(myIP string) {
    // 3b. 访问对端虚拟 IP 上的服务（整包走 iptunnel/1.0）
    peerIP := "10.0.0.5" // 由 p2put 集群 / StringToHostPart 得到
    conn, err := softun.Device().Dial("tcp", peerIP+":8080")
    if err != nil { panic(err) }
    conn.Write([]byte("hello"))
    conn.Close()

    // 3c. HTTP / DNS 走虚拟 LAN
    client := softun.Device().HTTPClient()
    resp, _ := client.Get("http://" + peerIP + ":8080/")
    fmt.Println(resp.StatusCode)

    // ── 4. loportmap 使用示意 ──
    // 本机起一个只监听 127.0.0.1 的本地服务（无需改代码）：
    go func() {
        l, err := net.Listen("tcp", "127.0.0.1:9000") // 注意：127.0.0.1，非虚拟栈
		if err != nil {
			log.Panicln(err)
		}
        for {
            c, _ := l.Accept()
            go func(c net.Conn) { defer c.Close(); c.Write([]byte("local-service\n")) }(c)
        }
    }()
    // 之后任意节点（或本机 vc 栈）连 10.0.0.17:9000 即可到达这个本地服务：
    time.Sleep(time.Second)
    c2, err := softun.Device().Dial("tcp", myIP+":9000") // 虚拟IP → 代理到 127.0.0.1:9000
    if err != nil { panic(err) }
    defer c2.Close()

}

func softunAcceptLoop(ln net.Listener) {
    for {
        c, err := ln.Accept()
        if err != nil { return }
		log.Println("new softun conn", c.RemoteAddr())
        go func(c net.Conn) { defer c.Close(); io.Copy(c, c) }(c)
    }
}
