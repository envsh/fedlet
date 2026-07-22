package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
	"github.com/envsh/libp2px/dlog"
	"github.com/envsh/libp2px/p2put"
	"github.com/envsh/libp2px/pbecho"
	"github.com/envsh/libp2px/pbexec"
	"github.com/envsh/libp2px/pbtunnel"
)

var svccaps = serviceCapacities{}

type serviceCapacities struct {
	sendMessage    bool
	bookmark       bool
	clipboard      bool
	sqliteStore    bool
	joplinInstance bool
	filesyncPoint  bool
	forignProxy    bool
	hasDE          bool
	langServer     bool
	aiagentServer  bool
	ocrServer      bool
}

var syncDir string

var publishViaHTTP bool = true
var channel_name = "reddit"
var ntfyshTopic string
var ntfyshServer string

func publishNtfy(protocol, channel string, v any) {
	if ntfyshTopic == "" {
		return
	}
	var body string
	var title string
	switch vv := v.(type) {
	case fbshared.UnifiedMessage:
		title = protocol + ":" + channel
		bcc, err := json.Marshal(vv)
		if err != nil { panic(err) }
		body = string(bcc)
	default:
		data, _ := json.Marshal(v)
		title = protocol + ":" + channel
		body = string(data)
		panic("not support")
	}
	if body == "" {
		return
	}
	url := ntfyshServer + "/" + ntfyshTopic + "?up=1"
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		log.Printf("ntfysh: request error: %v", err)
		return
	}
	req.Header.Set("Title", title)
	req.Header.Set("Tags", protocol)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("ntfysh: publish error: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("ntfysh: status %d body=%s len=%v", resp.StatusCode,  strings.TrimSpace(string(respBody)), len(body))
	}
}

func publish(protocol, channel string, v any) error {
	switch vv := v.(type) {
	// 是否要使用后端解析还是UI端解析呢,后端解析部署麻烦
	// 这个server层还是只做数据拉取,原样返回UI客户端,忽略统一化解析
	case fbshared.UnifiedMessage:
		data, err := json.Marshal(vv)
		if err != nil {
			return fmt.Errorf("publish: marshal UnifiedMessage: %w", err)
		}
		if len(data) == 0 {
			return fmt.Errorf("publish: empty UnifiedMessage")
		}
		btime := time.Now()
		if false {
			err = publishBytes(channel, data)
		}
		if err != nil {
			log.Println(channel, len(data), time.Since(btime), err)
		}
		log.Printf("publish: %s protocol=%s msgtype=%s msgid=%s format=%s chat=%s/%s user=%s/%s len(text)=%d attachments=%d",
			channel, vv.Protocol, vv.MsgType, vv.MsgID, vv.MsgFormat,
			vv.ChatID, vv.ChatName,
			vv.UserID, vv.Username,
			len(vv.Text), len(vv.Attachments))
		publishNtfy(protocol, channel, vv)
		return err

	case []byte:
		panic(fmt.Sprintf("publish: raw []byte not allowed (channel=%s, len=%d)", channel, len(vv)))

	default:
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("publish: marshal %T: %w", v, err)
		}
		os.WriteFile("/tmp/fedbrg_"+protocol+".json", data, 0644)
		if len(data) == 0 {
			return fmt.Errorf("publish: empty data")
		}
		btime := time.Now()
		err = publishBytes(channel, data)
		if err != nil {
			log.Println(channel, len(data), time.Since(btime), err)
		}
		// publishNtfy(protocol, channel, v)
		return err
	}
}
func publishBytes(channel string, data []byte) error {
	if publishViaHTTP {
		url := fmt.Sprintf("http://127.0.0.1:4004/p2pin/send?topic=%s", channel)
		resp, err := http.Post(url, "application/octet-stream", bytes.NewReader(data))
		if err != nil {
			return err
		}
		bcc, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		// log.Println(resp.StatusCode, url)
		if resp.StatusCode != 200 {
			return fmt.Errorf("%v %v", resp.StatusCode, string(bcc))
		}
		return nil
	}
	return p2put.PublishTopic(channel, data)
}

var DDLog = dlog.DDLog

func main() {
	cfg, p2putFs := p2put.ConfigFlags()
	cfg.Dht = false

	p2putFs.VisitAll(func(f *flag.Flag) {
		flag.Var(f.Value, f.Name, f.Usage)
	})
	flag.StringVar(&usepeer, "peerno", usepeer, "use which peer as tunnel dest, suffix 5 chars")
	flag.StringVar(&vlanpfx, "vlan", vlanpfx, "tun vlan ip prefix")
	flag.StringVar(&ntfyshTopic, "ntfysh-topic", "", "ntfy.sh topic for dual-publish (empty=disabled)")
	flag.StringVar(&ntfyshServer, "ntfysh-server", "https://ntfy.sh", "ntfy.sh server URL")
	flag.Parse()

	// ntfy.sh 参数校验
	if ntfyshTopic != "" {
		if len(ntfyshTopic) > 64 {
			log.Fatalf("ntfysh-topic: 长度超过 64 字符: %q", ntfyshTopic)
		}
		for _, c := range ntfyshTopic {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '-' || c == '_') {
				log.Fatalf("ntfysh-topic: 包含非法字符 %q (只允许字母数字和 - _)", ntfyshTopic)
			}
		}
	}
	if ntfyshServer != "" {
		u, err := url.Parse(ntfyshServer)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			log.Fatalf("ntfysh-server: 无效的 URL %q (需要 http:// 或 https://)", ntfyshServer)
		}
	}

	defer DDLog.ExitFlush()

	initVirTun(cfg.KeyFile)
	defer cleanupDarwinRoutes()

	go func() {
		if tunov == nil {
			return
		}
		for {
			board, err := p2put.CollectBoard()
			if err == nil {
				localPeerID = board.PeerID
				hostPart := stringToHostPart(board.PeerID)
				ip := vlanpfx + strconv.Itoa(hostPart)
				localPeerIP = ip
				log.Printf("virtun: computed IP from peer_id: %s", ip)
				if err := setupSeedVirtIP(ip); err != nil {
					log.Printf("virtun: %v", err)
				} else {
					log.Printf("virtun: %s configured and up from peer_id", ip)
				}
				if ipv6Available() {
					for _, pfx := range ipv6Prefixes {
						addr := pfx + strconv.Itoa(hostPart)
						if localPeerIPv6 == "" {
							localPeerIPv6 = addr
						}
						if err := setupSeedVirtIP(addr); err != nil {
							log.Printf("virtun: %s: %v", addr, err)
						} else {
							log.Printf("virtun: %s configured and up", addr)
						}
					}
				} else {
					log.Printf("virtun: IPv6 not available (kernel disabled), skipping")
				}
				return
			}
			log.Printf("virtun: collect board: %v (retry in 1s)", err)
			time.Sleep(time.Second)
		}
	}()

	for _, info := range ProtocolStatuses() {
		if info.StartFn != nil {
			info.StartFn()
		}
	}

	p2put.InstallRestHandler("/p2pin", nil)
	go p2put.MainLibp2p(*cfg)

	// go poll_toxrest()
	go poll_demopub()
	// go poll_gomuks()
	// go echoLoop()
	go tunloop()

	proxy := pbtunnel.NewHTTPProxy()
	go func() {
		err := proxy.ListenAndServe(":9229")
		log.Println(err)
		// 	panic(err)
	}()
	defer proxy.Close()

	err := http.ListenAndServe(":4004", nil)
	if err != nil {
		log.Println(err)
	}
}

/*
结论: gzip 压缩和 HTTP Range 请求（Accept-Ranges, If-Range, Range 头等）是互斥的。
结论：net/http 标准库没有内建的自动 gzip 压缩中间件。
所以最终还是需要引入一个外部包或用标准库自己写一个 wrapper。之前讨论的几个选择依然成立：
1. github.com/NYTimes/gziphandler v1.1.1 — 最轻量，只依赖 stdlib，Go 1.11+ 兼容，一行代码集成
2. 自己写 ~30 行中间件 — 用 compress/gzip + sync.Pool，零外部依赖
3. github.com/klauspost/compress/gzhttp — 性能更好但依赖较重

err := http.ListenAndServe(":4004", gzip.GzipHandler(http.DefaultServeMux))
*/

func tunloop() {
	var peerid string

	srv := pbtunnel.NewDriftServer(peerid)
	driftsrv = srv
	go waitPeerCome(srv, peerid)
	log.Println("Listen on :9339 =>", peerid)
	err := srv.Listen(":9339")
	log.Println(err)
	if err == nil {
	}
	select {}
}

func waitPeerCome(srv *pbtunnel.DriftServer, peerid string) {
	btime := time.Now()
	for peerid == "" && usepeer != "" {
		time.Sleep(2 * time.Second)
		pl := getPeerList()
		for _, p := range pl {
			if strings.HasSuffix(p.ID, usepeer) {
				peerid = p.ID
				srv.SwitchPeer(peerid)
				currentPeerID = peerid
				log.Println("swito peered ", peerid, time.Since(btime))
				break
			}
		}
	}
}

var driftsrv *pbtunnel.DriftServer
var usepeer = "NtYLT" // default; index into dynamic PeerDB list
var vlanpfx = "10.0.0."

func echoLoop() {
	peerid := ""
	for i := 0; ; i++ {
		time.Sleep(8 * time.Second)
		msg := fmt.Sprintf("hello foo %v", i)

		btime1 := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ret, err := pbecho.Echo(peerid, msg, ctx)
		log.Println(ret, err, p2put.IsPeerConnected(peerid, true) || p2put.IsPeerConnected(peerid, false), time.Since(btime1))

		time.Sleep(2 * time.Second)
		btime2 := time.Now()
		ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
		// res, err := pbexec.Exec(peerid, "uptime && uname -a", ctx2)
		res, err := pbexec.Exec(peerid, "uptime && uname -a && curl google.com", ctx2)
		log.Println(res, err, time.Since(btime2))

		cancel()
		cancel2()
	}
}
