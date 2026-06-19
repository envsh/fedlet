package main

import (
	"errors"
	"log"
	"context"
	"time"

	"golang.design/x/clipboard"
)

// need CGO_ENABLED=1 or panic for clipboard@v0.7.0 on X11
var clip_ierr = errors.New("unknown")
func init() {
	err := clipboard.Init()
	if err != nil {
		log.Println(err)
	}
	clip_ierr = err
}

func clipWaitProc() {
	chText := clipboard.Watch(context.TODO(), clipboard.FmtText)
	chImage := clipboard.Watch(context.TODO(), clipboard.FmtImage)
	btime := time.Now()
	defer func() { log.Println("done", time.Since(btime)) }()

	for {
		select {
		case data, ok := <-chText:
			if !ok {
				log.Println("clip watch failed", clipboard.FmtText)
				return
			}
			scc := string(data)
            log.Println("text:", len(scc), scc)
		case data, ok := <-chImage:
			if !ok {
				log.Println("clip watch failed", clipboard.FmtText)
				return
			}
            log.Println("image:", len(data), clipboard.FmtImage)
		}
	}
}

// func clipWaitProcV8() {
// 	ch := clipboard.Watch(context.TODO())
// 	for data := range ch {
// 		switch data.Format {
// 		case clipboard.FmtText:
// 			scc := string(data.Bytes)
//             log.Println("text:", len(scc), scc)
// 		case clipboard.FmtImage:
//             log.Println("image bytes:", len(data.Bytes), data.Format)
// 		default:
// 			log.Println("wt clip type", data.Format)
// 		}
// 	}
// }
