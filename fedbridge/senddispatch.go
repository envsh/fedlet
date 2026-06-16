package main

import (
	"fmt"
	"log"
)

var senders = make(map[string]func(to, msg, msgType string) error)

func RegisterSender(key string, fn func(to, msg, msgType string) error) {
	senders[key] = fn
}

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
