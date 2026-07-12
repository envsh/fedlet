package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

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

var publishViaHTTP bool = true
var channel_name = "reddit"

func publish(channel string, data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("publish: empty data")
	}
	btime := time.Now()
	err := publish2(channel, data)
	if err != nil {
		log.Println(channel, len(data), time.Since(btime), err)
	}
	// log.Println(channel, len(data), time.Since(btime), err)
	return err
}
func publish2(channel string, data []byte) error {
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
	flag.Parse()

	defer DDLog.ExitFlush()

	initVirTun(cfg.KeyFile)
	defer cleanupPFRules()

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
