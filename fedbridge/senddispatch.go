package main

import "fmt"

var senders = make(map[string]func(to, msg, msgType string) error)

func RegisterSender(protocol string, fn func(to, msg, msgType string) error) {
	senders[protocol] = fn
}

func DispatchSend(protocol, to, msg, msgType string) error {
	fn, ok := senders[protocol]
	if !ok {
		return fmt.Errorf("senddispatch: unknown protocol %q", protocol)
	}
	return fn(to, msg, msgType)
}
