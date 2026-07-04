//go:build !android

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"hash/crc64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"context"
	"net/http"
	"sync"
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
	if err == nil {
		go clipWaitProc()
	}
}

const clipDedupInterval = 2 * time.Second

var (
	lastClipText   string
	lastClipTextAt time.Time
	lastClipImgKey uint64
	lastClipImgAt  time.Time
	clipMu         sync.Mutex
)

func clipDedupText(s string) bool {
	clipMu.Lock()
	defer clipMu.Unlock()
	now := time.Now()
	if s == lastClipText && now.Sub(lastClipTextAt) < clipDedupInterval {
		return false
	}
	lastClipText = s
	lastClipTextAt = now
	return true
}

func clipDedupImage(data []byte) bool {
	clipMu.Lock()
	defer clipMu.Unlock()
	now := time.Now()
	k := crc64.Checksum(data, crc64.MakeTable(crc64.ISO))
	if k == lastClipImgKey && now.Sub(lastClipImgAt) < clipDedupInterval {
		return false
	}
	lastClipImgKey = k
	lastClipImgAt = now
	return true
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
			if !clipDedupText(scc) {
				log.Printf("clipboard: dedup text %q within 2s, skip", scc)
			} else {
				publish(channel_name, marshalClipEvent("text", "", scc, 0, 0, 0))
			}
		case data, ok := <-chImage:
			if !ok {
				log.Println("clip watch failed", clipboard.FmtText)
				return
			}
			log.Println("image:", len(data), clipboard.FmtImage)
			mime := http.DetectContentType(data)
			cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
			w, h := 0, 0
			if err == nil {
				w, h = cfg.Width, cfg.Height
			} else {
				log.Printf("clipboard: decode image config: %v", err)
			}
			if !clipDedupImage(data) {
				log.Printf("clipboard: dedup image within 2s, skip")
			} else {
				publish(channel_name, marshalClipEvent("image", mime, "", len(data), w, h))
			}
		}
	}
}

func marshalClipEvent(format, mime, text string, size, width, height int) []byte {
	b, _ := json.Marshal(struct {
		Type   string `json:"type"`
		Format string `json:"format"`
		MIME   string `json:"mime,omitempty"`
		Data   string `json:"data,omitempty"`
		Size   int    `json:"size,omitempty"`
		Width  int    `json:"width,omitempty"`
		Height int    `json:"height,omitempty"`
	}{
		Type:   "clipboard",
		Format: format,
		MIME:   mime,
		Data:   text,
		Size:   size,
		Width:  width,
		Height: height,
	})
	return b
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
