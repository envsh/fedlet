package main

import (
	"fmt"
	"log"
)

var senders = make(map[string]func(to, msg, msgType string) error)

func RegisterSender(key string, fn func(to, msg, msgType string) error) {
	senders[key] = fn
}

// DispatchSend 按联系人类型分发消息。
//   ctype:    联系人类型常量（如 "unktox_conference"），用于查找到哪个后端 sender
//   to:       目标标识（friend ID / room ID 等）
//   msg:      消息正文
//   msgType:  传给 sender 的消息类型参数（后端用它做更细分的路由，如旧 tox API 区分 friend/conference/group）
func DispatchSend(ctype, to, msg, msgType string) error {
	fn, ok := senders[ctype]
	log.Printf("senddispatch: ctype=%q to=%q msg=%q sender_ok=%v", ctype, to, msg, ok)
	if !ok {
		return fmt.Errorf("senddispatch: unknown contact type %q", ctype)
	}
	err := fn(to, msg, msgType)
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
)
