//go:build emailimap

package main

import (
	"encoding/json"
	"flag"
	"os"

	"github.com/envsh/fedlet/fbprotocols/emailimap"
)

var (
	emailAuth   string
	emailImapDir string
	emailImapServer string
)

func init() {
	flag.StringVar(&emailAuth, "emailauth", "", "IMAP user:password (or EMAILAUTH env var)")
	flag.StringVar(&emailImapDir, "imapdir", "INBOX,Sent", "IMAP folders (comma-separated)")
	flag.StringVar(&emailImapServer, "imapserver", "outlook.office365.com:993", "IMAP server (host[:port])")
	starters = append(starters, func() {
		auth := emailAuth
		if auth == "" {
			auth = os.Getenv("EMAILAUTH")
		}
		cfg := emailimap.Config{
			Auth:   auth,
			Dir:    emailImapDir,
			Server: emailImapServer,
		}
		b, _ := json.Marshal(cfg)
		emailimap.SetPublishInfo(func(data []byte) error {
			return publish(channel_name, data)
		})
		emailimap.Start(string(b))
	})
}
