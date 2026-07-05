package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	// "strings"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
	"github.com/envsh/libp2px/pbtunnel"
	// "github.com/envsh/libp2px/p2put"
)

type forwardReq struct {
	Cmd      string                  `json:"__cmd__"`
	Ctype    string                  `json:"ctype"`
	To       string                  `json:"to"`
	Msg      string                  `json:"msg"`
	MsgType  string                  `json:"msgType"`
	Filedata []byte                  `json:"filedata,omitempty"`
	FileInfo *fbshared.MediaDataInfo `json:"fileinfo,omitempty"`
}

// DispatchSend 按联系人类型分发消息。
//   ctype:    联系人类型常量（如 "unktox_conference"），用于查找到哪个后端 sender
//   to:       目标标识（friend ID / room ID 等）
//   msg:      消息正文
//   msgType:  传给 sender 的消息类型参数（后端用它做更细分的路由，如旧 tox API 区分 friend/conference/group）
//   filedata: 文件字节流，nil 表示纯文本
//   fileinfo: 文件元信息，nil 表示无附件
func DispatchSend(ctype, to, msg, msgType string, filedata []byte, fileinfo *fbshared.MediaDataInfo) error {
	info, ok := ctypeRegistry[ctype]
	log.Printf("senddispatch: ctype=%q to=%q msg=%q ok=%v canSend=%v",
		ctype, to, msg, ok, ok && info.Capacities.CanSend)
	if !ok {
		req := forwardReq{Cmd: "forward", Ctype: ctype, To: to, Msg: msg, MsgType: msgType, Filedata: filedata, FileInfo: fileinfo}
		data, _ := json.Marshal(req)
		log.Printf("senddispatch: forwardReq=%s", data)
		return fmt.Errorf("senddispatch: no local sender for %q", ctype)
	}
	if info.statusFn != nil {
		st := info.Status()
		log.Printf("senddispatch: protocol=%q running=%v connected=%v reconn=%d errs=%d",
			info.Name, st.Running, st.ConnectedSince, st.ReconnTimes, len(st.LastErrs))
		if !st.Running || (st.Running && st.ConnectedSince.IsZero()) {
			req := forwardReq{Cmd: "forward", Ctype: ctype, To: to, Msg: msg, MsgType: msgType, Filedata: filedata, FileInfo: fileinfo}
			data, _ := json.Marshal(req)
			log.Printf("senddispatch: backend %q not ready (running=%v connected=%v), forwardReq=%s",
				info.Name, st.Running, st.ConnectedSince, data)
			return fmt.Errorf("senddispatch: backend %q not ready", ctype)
		}
	}
	err := info.SendFn(to, msg, msgType, filedata, fileinfo)
	log.Printf("senddispatch: ctype=%q result=%v", ctype, err)
	return err
}

// 联系人类型常量（与 toxhttpd/qltox/eventpoller.cpp 定义一致）
const (
	TypeImapMail      = "imap_mail"
	TypeGomuksRoom    = "gomuks_room"
	TypeToxFriend     = "unktox_friend"
	TypeToxConference = "unktox_conference"
	TypeToxGroup      = "unktox_group"
	TypeSysEvent      = "sysevent"
	TypeTopic         = "topic"
	TypeUnknown       = "unknown"
	TypeIRCCloud      = "irccloud"
	TypeLounge        = "irclounge"
	TypeMatrix        = "matrix"
	TypeMisskeyNote   = "misskey_note"
	TypeOutlookEvent  = "outlook_event"
)

// return when first success
func ForeachSend(ctype, to, msg, msgType string, filedata []byte, fileinfo *fbshared.MediaDataInfo) error {
	var err0 error
	btime := time.Now()
	pl := getPeerList()
	if len(pl) == 0 {
		return fmt.Errorf("foreachsend: no peers available")
	}
	for _, p := range pl {
		peerid := p.ID
		log.Println("swito peered ", peerid, time.Since(btime))
		htcli := pbtunnel.NewHttpClient(peerid)

		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		w.WriteField("type", msgType)
		w.WriteField("id", to)
		w.WriteField("message", msg)
		if len(filedata) > 0 {
			filename := "file"
			if fileinfo != nil && fileinfo.Filename != "" {
				filename = fileinfo.Filename
			}
			fw, _ := w.CreateFormFile("file", filename)
			fw.Write(filedata)
		}
		w.Close()

		req, err := http.NewRequest(http.MethodPost,
			"http://127.0.0.1:4004/api/messages/send", &buf)
		if err != nil {
			err0 = err
			log.Println(err, peerid)
			continue
		}
		req.Header.Set("Content-Type", w.FormDataContentType())

		resp, err := htcli.Do(req)
		err0 = err
		if resp != nil {
			slurp, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if len(slurp) > 99 {
				slurp = slurp[:99]
			}
			log.Println("rethttp:", string(slurp))
		}
		if err0 != nil {
			log.Println(err0, peerid)
		} else {
			break
		}
	}
	return err0
}
