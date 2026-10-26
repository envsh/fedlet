//go:build !android

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"hash/crc64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"net/http"
	"sync"
	"time"
	"fmt"
	"os"
	"runtime"

	"golang.design/x/clipboard"
	
	"github.com/envsh/fedlet/fbprotocols/fbshared"
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
	var err error
	_ = err

	for {
		select {
		case data, ok := <-chText:
			if !ok {
				log.Println("clipboard: clip watch failed", clipboard.FmtText)
				return
			}
			scc := string(data)
			log.Printf("clipboard: pid=%d name=%s text: %d %s", -1, "", len(scc), scc)
			if !clipDedupText(scc) {
				log.Printf("clipboard: dedup text %q within 2s, skip", scc)
			} else {
				pid, appName, appPath := clipOwner()
				log.Printf("clipboard: owner pid=%d name=%s path=%s", pid, appName, appPath)
				ba, obj := marshalClipEvent("text", "", scc, 0, 0, 0, pid, appName, appPath)
				err = publish("clipboard", channel_name, json.RawMessage(ba))
				umsg := cbPlainDataToUnified(ba, obj)
				err = publish("clipboard", channel_name, umsg)
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
				pid, appName, appPath := clipOwner()
				log.Printf("clipboard: owner pid=%d name=%s path=%s", pid, appName, appPath)
				ba, obj := marshalClipEvent("image", mime, "", len(data), w, h, pid, appName, appPath)
				err = publish("clipboard", channel_name, json.RawMessage(ba))
				umsg := cbPlainDataToUnified(ba, obj)
				if false {
					err = publish("clipboard", channel_name, umsg)
				}
			}
		}
	}
}

func marshalClipEvent(format, mime, text string, size, width, height, pid int, appName, appPath string) ([]byte,  ClipboardPlainData) {
	o := ClipboardPlainData{
		Type:    "clipboard",
		Format:  format,
		MIME:    mime,
		Data:    text,
		Size:    size,
		Width:   width,
		Height:  height,
		PID:     pid,
		AppName: appName,
		AppPath: appPath,
	}
	b, _ := json.Marshal(o)
	return b, o
}

type ClipboardPlainData struct {
	Type    string `json:"type"`
	Format  string `json:"format"`
	MIME    string `json:"mime,omitempty"`
	Data    string `json:"data,omitempty"`
	Size    int    `json:"size,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	PID     int    `json:"pid,omitempty"`
	AppName string `json:"appname,omitempty"`
	AppPath string `json:"apppath,omitempty"`
}

func cbPlainDataToUnified(ba []byte, obj ClipboardPlainData) fbshared.UnifiedMessage {
	var res = fbshared.UnifiedMessage{}
	res.Text = fmt.Sprintf("%vx%v %v %s %s", obj.Width, obj.Height, obj.Size, obj.AppName, obj.Data)
	res.MsgFormat = obj.Format
	
	res.Protocol = obj.Type
	res.ChatID = obj.Type
	res.ChatName = "clipboard 剪贴板"
	
	nowt := time.Now().UnixNano()
	hname, _ := os.Hostname()
	res.Username = fmt.Sprintf("%s-%s-%s", runtime.GOOS, runtime.GOARCH, hname) // "hostname"
	res.UserID = res.Username
	res.UserIcon = ""
	res.MsgType = obj.MIME
	res.MsgID = fmt.Sprintf("CBP%v-%v", obj.PID, nowt)
	res.Timestamp = nowt
	
	return res
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
