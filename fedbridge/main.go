package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	// "os"
	"time"
	"context"

	"github.com/envsh/libp2px/p2put"
	"github.com/envsh/libp2px/pbecho"
	"github.com/envsh/libp2px/pbexec"
	"github.com/envsh/libp2px/pbtunnel"
)

var publishViaHTTP bool = true
var channel_name = "reddit"

func publish(channel string, data []byte) error {
	if publishViaHTTP {
		url := fmt.Sprintf("http://127.0.0.1:4004/p2pin/send?topic=%s", channel)
		resp, err := http.Post(url, "application/octet-stream", bytes.NewReader(data))
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}
	return p2put.PublishTopic(channel, data)
}

func main() {
	cfg := p2put.ParseConfig()
	cfg.Dht = false
	_ = cfg

	// err1 := cfg.Fset.Parse(os.Args[1:])
	// if err1 != nil {
	// 	// log.Println(err1)
	// 	return
	// }
	// cfg.KeyFile = *keyFile
	// cfg.ListenPort = *port

	go p2put.MainLibp2p(cfg)
	p2put.InstallRestHandler("/p2pin", nil)

	// go poll_toxrest()
	go poll_demopub()
	// go poll_gomuks()
	// go echoLoop()
	go tunloop()

	// proxy := pbtunnel.NewHTTPProxy()
	// go func() {
	// 	err := proxy.ListenAndServe(":9449")
	// 	log.Println(err)
	// 	panic(err)
	// }()
	// defer proxy.Close()

	err := http.ListenAndServe(":4004", nil)
	if err != nil {
		log.Println(err)
	}
}

func tunloop() {
	peerid := peerid2

	srv := pbtunnel.NewDriftServer(peerid)
	log.Println("Listen on :9339 =>", peerid)
	err := srv.Listen(":9339")
	log.Println(err)
	select{}
}

var peerid0 = "12D3KooWHXjoE8cMhPPD7JaUGHHiXCNLHQcbgUQrXFc788oq6ahm"
var peerid1 = "12D3KooWDVExaeKp1YzYvhS7E6oZDdDnEB3HENS9VrYp3vKME7m1"
var peerid2 = "12D3KooWSgyQhqayreZ6UequLq3ZGxJm1WG4tyszD29ps8zNtYLT"

func echoLoop() {
	peerid := peerid1
	for i := 0; ; i++ {
		time.Sleep(8*time.Second)
		msg := fmt.Sprintf("hello foo %v", i)

		btime1 := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ret, err := pbecho.Echo(peerid, msg, ctx)
		log.Println(ret, err, p2put.IsPeerConnected(peerid, true)||p2put.IsPeerConnected(peerid, false), time.Since(btime1))

		time.Sleep(2*time.Second)
		btime2 := time.Now()
		ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
		// res, err := pbexec.Exec(peerid, "uptime && uname -a", ctx2)
		res, err := pbexec.Exec(peerid, "uptime && uname -a && curl google.com", ctx2)
		log.Println(res, err, time.Since(btime2))

		cancel()
		cancel2()
	}
}
