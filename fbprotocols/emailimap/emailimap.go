package emailimap

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap-id"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/commands"
	"github.com/emersion/go-sasl"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

type Config struct {
	Auth   string `json:"auth"`
	Dir    string `json:"dir"`
	Server string `json:"server"`
	SMTP   string `json:"smtp"`
	From   string `json:"from"`
}

type messageData struct {
	ID               string   `json:"id"`
	Subject          string   `json:"subject"`
	From             string   `json:"from"`
	ToRecipients     []string `json:"toRecipients"`
	CcRecipients     []string `json:"ccRecipients,omitempty"`
	BodyPreview      string   `json:"bodyPreview"`
	BodyHtml         string   `json:"bodyHtml,omitempty"`
	ReceivedDateTime string   `json:"receivedDateTime"`
	FolderID         string   `json:"folderId"`
	FolderName       string   `json:"folderName"`
	Charset          string   `json:"charset"`
}

type stateData struct {
	Folders map[string]uint32 `json:"folders"`
}

var (
	publishFn func([]byte) error
	muSend    sync.Mutex
	smtpAddr  string
	smtpUser  string
	smtpPass  string
	mailFrom  string
)

func SetPublishInfo(fn func([]byte) error) {
	publishFn = fn
}

func publish(data []byte) error {
	if publishFn == nil {
		return fmt.Errorf("emailimap: publish fn not set")
	}
	return publishFn(data)
}

func Start(info string) {
	var cfg Config
	if err := json.Unmarshal([]byte(info), &cfg); err != nil {
		log.Println("emailimap: config error:", err)
		return
	}

	parts := strings.SplitN(cfg.Auth, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		log.Println("emailimap: --emailauth must be user:password")
		return
	}
	username, password := parts[0], parts[1]

	dirs := strings.Split(cfg.Dir, ",")
	for i := range dirs {
		dirs[i] = strings.TrimSpace(dirs[i])
	}

	server := cfg.Server
	if !strings.Contains(server, ":") {
		server += ":993"
	}

	smtpSrv := cfg.SMTP
	if smtpSrv == "" {
		smtpSrv = deriveSMTPServer(server)
	}
	from := cfg.From
	if from == "" {
		from = deriveFrom(username)
	}

	muSend.Lock()
	smtpAddr = smtpSrv
	smtpUser = username
	smtpPass = password
	mailFrom = from
	muSend.Unlock()

	go poll(username, password, server, dirs)
}

func stateFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fedlet", "imap-state.json")
}

func loadState() *stateData {
	data, err := os.ReadFile(stateFilePath())
	if err != nil {
		return &stateData{Folders: make(map[string]uint32)}
	}
	var s stateData
	json.Unmarshal(data, &s)
	if s.Folders == nil {
		s.Folders = make(map[string]uint32)
	}
	return &s
}

func saveState(s *stateData) {
	data, _ := json.MarshalIndent(s, "", "  ")
	p := stateFilePath()
	os.MkdirAll(filepath.Dir(p), 0700)
	os.WriteFile(p, data, 0600)
}

func getBodyPreview(mailID string, r io.Reader) (preview, html, charset string) {
	if r == nil {
		return "", "", ""
	}

	msg, err := mail.ReadMessage(r)
	if err != nil {
		return "", "", ""
	}

	cte := msg.Header.Get("Content-Transfer-Encoding")
	log.Printf("emailimap: mail %s CTE=%q type=%q", mailID, cte, msg.Header.Get("Content-Type"))
	body := decodeCTE(msg.Body, cte)

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		mediaType = "text/plain"
	}

	if strings.HasPrefix(mediaType, "text/plain") {
		cs := params["charset"]
		return readTextBody(body, cs), "", cs
	}

	if strings.HasPrefix(mediaType, "text/html") {
		cs := params["charset"]
		return "", readTextBodyFull(body, cs), cs
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		mr := multipart.NewReader(msg.Body, params["boundary"])
		var htmlBody, htmlCharset string
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			pmt, pmp, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			pcte := part.Header.Get("Content-Transfer-Encoding")
			log.Printf("emailimap: mail %s multipart part CTE=%q type=%q", mailID, pcte, pmt)
			pBody := decodeCTE(part, pcte)
			if strings.HasPrefix(pmt, "text/plain") {
				cs := pmp["charset"]
				return readTextBody(pBody, cs), "", cs
			}
			if strings.HasPrefix(pmt, "text/html") && htmlBody == "" {
				htmlBody = readTextBodyFull(pBody, pmp["charset"])
				htmlCharset = pmp["charset"]
			}
		}
		if htmlBody != "" {
			return "", htmlBody, htmlCharset
		}
	}

	return "", "", ""
}

func readTextBodyFull(r io.Reader, charset string) string {
	body, _ := io.ReadAll(r)
	text := string(body)
	if c, ok := charsetDecoder(charset); ok {
		dec := transform.NewReader(strings.NewReader(text), c.NewDecoder())
		if b, err := io.ReadAll(dec); err == nil {
			text = string(b)
		} else {
			log.Printf("emailimap: decode error for charset %q: %v", charset, err)
		}
	}
	return strings.TrimSpace(text)
}

func readTextBody(r io.Reader, charset string) string {
	body, _ := io.ReadAll(r)
	text := string(body)
	if c, ok := charsetDecoder(charset); ok {
		dec := transform.NewReader(strings.NewReader(text), c.NewDecoder())
		if b, err := io.ReadAll(dec); err == nil {
			text = string(b)
		} else {
			log.Printf("emailimap: decode error for charset %q: %v", charset, err)
		}
	}
	return truncate(strings.TrimSpace(text), 686)
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

func decodeCTE(r io.Reader, cte string) io.Reader {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, r)
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	default:
		return r
	}
}

func charsetDecoder(name string) (encoding.Encoding, bool) {
	switch strings.ToLower(name) {
	case "utf-8", "utf8":
		return nil, false
	case "iso-8859-1", "latin1":
		return charmap.ISO8859_1, true
	case "windows-1252":
		return charmap.Windows1252, true
	case "shift_jis", "shift-jis":
		return japanese.ShiftJIS, true
	case "iso-2022-jp":
		return japanese.ISO2022JP, true
	case "euc-jp":
		return japanese.EUCJP, true
	case "gb2312", "gbk", "gb18030":
		return simplifiedchinese.GB18030, true
	case "big5":
		return traditionalchinese.Big5, true
	case "euc-kr":
		return korean.EUCKR, true
	default:
		return nil, false
	}
}

func probeServer(c *client.Client) {
	caps, err := c.Capability()
	if err != nil {
		log.Printf("emailimap: capability probe error: %v", err)
		return
	}

	var keys []string
	for k := range caps {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	log.Printf("emailimap: ----- server capabilities (%d) -----", len(keys))
	for _, k := range keys {
		log.Printf("emailimap:   %s", k)
	}
	log.Printf("emailimap: -----------------------------------")
}

func sendClientID(c *client.Client) {
	ic := id.NewClient(c)
	ok, err := ic.SupportID()
	if err != nil {
		log.Printf("emailimap: ID probe error: %v", err)
		return
	}
	if !ok {
		log.Println("emailimap: server does not support ID command")
		return
	}

	clientID := id.ID{
		id.FieldName:    "Thunderbird",
		id.FieldVersion: "888",
	}
	serverID, err := ic.ID(clientID)
	if err != nil {
		log.Printf("emailimap: ID command error: %v", err)
		return
	}
	log.Printf("emailimap: sent client ID (name=Thunderbird, version=888)")
	for k, v := range serverID {
		log.Printf("emailimap:   server %s: %s", k, v)
	}
}

func loginWithFallback(c *client.Client, username, password string) error {
	disabled, err := c.Support("LOGINDISABLED")
	if err != nil {
		return fmt.Errorf("capability check error: %w", err)
	}

	if disabled {
		log.Println("emailimap: LOGINDISABLED set, bypassing with direct LOGIN")

		cmd := &commands.Login{Username: username, Password: password}
		status, err := c.Execute(cmd, nil)
		if err != nil {
			return fmt.Errorf("direct LOGIN failed: %w", err)
		}
		if err := status.Err(); err != nil {
			return fmt.Errorf("direct LOGIN rejected: %w", err)
		}

		c.SetState(imap.AuthenticatedState, nil)
		return nil
	}

	if err := c.Login(username, password); err != nil {
		log.Printf("emailimap: LOGIN failed (%v), trying AUTHENTICATE PLAIN", err)

		auth := sasl.NewPlainClient("", username, password)
		if err := c.Authenticate(auth); err != nil {
			return fmt.Errorf("all auth methods failed (LOGIN and AUTHENTICATE PLAIN): %w", err)
		}
		return nil
	}
	return nil
}

func poll(username, password, server string, dirs []string) {
	statusRunning.Store(true)
	statusConnectedSince.Store(time.Now())
	defer statusRunning.Store(false)
	log.Printf("emailimap: connecting to %s", server)

	c, err := client.DialTLS(server, nil)
	if err != nil {
		log.Println("emailimap: dial:", err)
		pushError(err)
		return
	}
	defer c.Logout()

	probeServer(c)

	sendClientID(c)

	if err := loginWithFallback(c, username, password); err != nil {
		log.Println("emailimap: login failed:", err)
		pushError(err)
		c.Logout()
		return
	}
	log.Println("emailimap: logged in")

	enabled, err := c.Enable([]string{"UTF8=ACCEPT"})
	if err != nil {
		log.Printf("emailimap: ENABLE UTF8=ACCEPT not supported: %v", err)
	} else {
		log.Printf("emailimap: ENABLE UTF8=ACCEPT enabled: %v", enabled)
	}

	state := loadState()

	for _, dir := range dirs {
		log.Printf("emailimap: syncing %s", dir)

		if _, err := c.Select(dir, true); err != nil {
			log.Printf("emailimap: select %s: %v", dir, err)
			continue
		}

		lastUID := state.Folders[dir]
		var uids []uint32

		if lastUID == 0 {
			all, err := c.UidSearch(imap.NewSearchCriteria())
			if err != nil {
				log.Printf("emailimap: search %s: %v", dir, err)
				continue
			}
			if len(all) > 10 {
				uids = all[len(all)-10:]
			} else {
				uids = all
			}
		} else {
			criteria := imap.NewSearchCriteria()
			criteria.Uid = new(imap.SeqSet)
			criteria.Uid.AddRange(lastUID+1, 0)
			uids, err = c.UidSearch(criteria)
			if err != nil {
				log.Printf("emailimap: search %s: %v", dir, err)
			} else {
				var filtered []uint32
				for _, uid := range uids {
					if uid > lastUID {
						filtered = append(filtered, uid)
					}
				}
				uids = filtered
			}
		}

		if len(uids) > 0 {
			msgs := fetchMessages(c, uids, dir)
			for _, m := range msgs {
				b, _ := json.Marshal(m)
				if err := publish(b); err != nil {
					log.Println("emailimap: publish error:", err)
				}
				um := m.toUnified(b)
				data, _ := json.Marshal(um)
				publish(data)
			}
			log.Printf("emailimap: %s: %d messages", dir, len(msgs))

			maxUID := lastUID
			for _, uid := range uids {
				if uid > maxUID {
					maxUID = uid
				}
			}
			state.Folders[dir] = maxUID
			saveState(state)
		}
	}

	log.Println("emailimap: initial sync complete, polling every 30s")

	for {
		time.Sleep(30 * time.Second)

		if err := c.Noop(); err != nil {
			log.Println("emailimap: reconnecting")
			c.Logout()
			c2, err := client.DialTLS(server, nil)
			if err != nil {
				log.Println("emailimap: reconnect dial:", err)
				pushError(err)
				continue
			}
			sendClientID(c2)
			if err := loginWithFallback(c2, username, password); err != nil {
				log.Println("emailimap: reconnect login:", err)
				pushError(err)
				c2.Logout()
				continue
			}
			c = c2
		}

		for _, dir := range dirs {
			if _, err := c.Select(dir, true); err != nil {
				continue
			}

			lastUID := state.Folders[dir]
			if lastUID == 0 {
				continue
			}

			criteria := imap.NewSearchCriteria()
			criteria.Uid = new(imap.SeqSet)
			criteria.Uid.AddRange(lastUID+1, 0)
			uids, err := c.UidSearch(criteria)
			if err != nil {
				log.Printf("emailimap: poll %s: UidSearch error: %v", dir, err)
				continue
			}
			var filtered []uint32
			for _, uid := range uids {
				if uid > lastUID {
					filtered = append(filtered, uid)
				}
			}
			uids = filtered
			if len(uids) == 0 {
				continue
			}

			log.Printf("emailimap: poll %s: lastUID=%d, uids=%v", dir, lastUID, uids)

			msgs := fetchMessages(c, uids, dir)
			for _, m := range msgs {
				b, _ := json.Marshal(m)
				if err := publish(b); err != nil {
					log.Println("emailimap: publish error:", err)
				}
			}

			maxUID := lastUID
			for _, uid := range uids {
				if uid > maxUID {
					maxUID = uid
				}
			}
			state.Folders[dir] = maxUID
			log.Printf("emailimap: poll %s: maxUID=%d", dir, maxUID)
			saveState(state)

		for _, m := range msgs {
			log.Printf("emailimap: %s: UID=%s subject=%q bodyLen=%d", dir, m.ID, m.Subject, len(m.BodyPreview))
		}
		}
	}
}

func fetchMessages(c *client.Client, uids []uint32, folder string) []messageData {
	seqset := new(imap.SeqSet)
	seqset.AddNum(uids...)

	var section imap.BodySectionName
	section.Specifier = imap.EntireSpecifier
	section.Peek = true
	section.Partial = []int{0, 65536}

	items := []imap.FetchItem{
		imap.FetchEnvelope,
		imap.FetchInternalDate,
		imap.FetchUid,
		section.FetchItem(),
	}

	msgs := make(chan *imap.Message, len(uids))
	done := make(chan error, 1)
	go func() {
		done <- c.UidFetch(seqset, items, msgs)
	}()

	var result []messageData
	for msg := range msgs {
		if msg.Envelope == nil {
			continue
		}

		m := messageData{
			ID:         fmt.Sprintf("%d", msg.Uid),
			FolderID:   folder,
			FolderName: folder,
		}
		if msg.Envelope.Subject != "" {
			m.Subject = decodeWord(msg.Envelope.Subject)
		}
		if len(msg.Envelope.From) > 0 && msg.Envelope.From[0] != nil {
			m.From = addrString(msg.Envelope.From[0])
		}
		for _, a := range msg.Envelope.To {
			if a != nil {
				m.ToRecipients = append(m.ToRecipients, addrString(a))
			}
		}
		for _, a := range msg.Envelope.Cc {
			if a != nil {
				m.CcRecipients = append(m.CcRecipients, addrString(a))
			}
		}
		if !msg.InternalDate.IsZero() {
			m.ReceivedDateTime = msg.InternalDate.Format(time.RFC3339)
		} else if !msg.Envelope.Date.IsZero() {
			m.ReceivedDateTime = msg.Envelope.Date.Format(time.RFC3339)
		}

		m.BodyPreview, m.BodyHtml, m.Charset = getBodyPreview(m.ID, msg.GetBody(&section))
		result = append(result, m)
	}

	if err := <-done; err != nil {
		log.Printf("emailimap: fetch: %v", err)
	}

	return result
}

func addrString(a *imap.Address) string {
	return a.MailboxName + "@" + a.HostName
}

func decodeWord(s string) string {
	dec := mime.WordDecoder{}
	r, err := dec.DecodeHeader(s)
	if err != nil {
		return s
	}
	return r
}

// resolveServer 自动检测 IMAP 服务器地址。
// 如果 server 非空，直接返回（用户手动指定，不检测）。
// 否则根据 email 域名自动发现：硬编码 → ISPDB → 猜想。
func resolveServer(email, server string) string {
	if server != "" {
		if !strings.Contains(server, ":") {
			server += ":993"
		}
		return server
	}
	domain := extractDomain(email)
	if domain == "" {
		return "outlook.office365.com:993"
	}

	if isMicrosoftDomain(domain) {
		return "outlook.office365.com:993"
	}

	if s := ispdbLookup(domain); s != "" {
		return s
	}

	return guessServer(domain)
}

func extractDomain(email string) string {
	_, domain, found := strings.Cut(email, "@")
	if !found {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(domain))
}

func isMicrosoftDomain(domain string) bool {
	switch domain {
	case "outlook.com", "hotmail.com", "live.com",
		"hotmail.co.uk", "hotmail.fr", "hotmail.de",
		"msn.com", "passport.com",
		"office365.com":
		return true
	}
	return false
}

type ispdbServer struct {
	Hostname string `xml:"hostname"`
	Port     int    `xml:"port"`
	Socket   string `xml:"socketType"`
}

type ispdbIncoming struct {
	Type    string       `xml:"type,attr"`
	Server  ispdbServer  `xml:",innerxml"`
}

type ispdbConfig struct {
	Incoming []ispdbIncoming `xml:"emailProvider>incomingServer"`
}

func ispdbLookup(domain string) string {
	url := "https://autoconfig.thunderbird.net/v1.1/" + domain
	resp, err := http.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var cfg ispdbConfig
	if err := xml.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return ""
	}

	for _, in := range cfg.Incoming {
		if in.Type == "imap" && in.Server.Socket == "SSL" && in.Server.Port > 0 {
			addr := fmt.Sprintf("%s:%d", in.Server.Hostname, in.Server.Port)
			if dialIMAP(addr) {
				return addr
			}
		}
	}

	return ""
}

func guessServer(domain string) string {
	for _, host := range []string{"imap." + domain, "mail." + domain} {
		addr := host + ":993"
		if dialIMAP(addr) {
			return addr
		}
	}
	return ""
}

func dialIMAP(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func deriveSMTPServer(imapServer string) string {
	host, portStr, err := net.SplitHostPort(imapServer)
	if err != nil {
		return "smtp." + imapServer + ":587"
	}
	host = strings.Replace(host, "imap.", "smtp.", 1)
	newPort := "587"
	if portStr == "143" {
		newPort = "25"
	}
	return net.JoinHostPort(host, newPort)
}

func deriveFrom(username string) string {
	if strings.Contains(username, "@") {
		return username
	}
	return username + "@unknown"
}

func Send(to, msg, msgType string, filedata []byte, fileinfo *fbshared.MediaDataInfo) error {
	if to == "" || msg == "" {
		return fmt.Errorf("emailimap: empty to or message")
	}
	muSend.Lock()
	addr := smtpAddr
	user := smtpUser
	pass := smtpPass
	from := mailFrom
	muSend.Unlock()

	if addr == "" {
		return fmt.Errorf("emailimap: SMTP not configured")
	}
	if from == "" {
		return fmt.Errorf("emailimap: from address not configured")
	}

	var recipients []string
	for _, r := range strings.Split(to, ",") {
		r = strings.TrimSpace(r)
		if strings.Contains(r, "@") {
			recipients = append(recipients, r)
		}
	}
	if len(recipients) == 0 {
		return fmt.Errorf("emailimap: no valid recipients")
	}

	subject := msgType
	if subject == "" {
		subject = "Message from fedlet"
	}

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("From: %s\r\n", from))
	buf.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(recipients, ", ")))
	buf.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	buf.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	buf.WriteString("\r\n")
	buf.WriteString(msg)

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("emailimap: invalid SMTP addr %q: %w", addr, err)
	}

	auth := smtp.PlainAuth("", user, pass, host)
	if err := smtp.SendMail(addr, auth, from, recipients, buf.Bytes()); err != nil {
		return fmt.Errorf("emailimap: %w", err)
	}
	return nil
}

// protocol status
var (
	statusRunning        atomic.Bool
	statusConnectedSince atomic.Value // time.Time
	statusReconnTimes    atomic.Int64
	statusLastErrsMu     sync.Mutex
	statusLastErrs       [3]error // ring buffer, [0]=newest
)

func pushError(err error) {
	statusLastErrsMu.Lock()
	statusLastErrs[2] = statusLastErrs[1]
	statusLastErrs[1] = statusLastErrs[0]
	statusLastErrs[0] = err
	statusLastErrsMu.Unlock()
}

func IsRunning() bool         { return statusRunning.Load() }
func ConnectedSince() time.Time {
	v := statusConnectedSince.Load()
	if v == nil { return time.Time{} }
	return v.(time.Time)
}
func ReconnTimes() int64      { return statusReconnTimes.Load() }
func LastErrs() []error {
	statusLastErrsMu.Lock()
	defer statusLastErrsMu.Unlock()
	var out []error
	for _, e := range statusLastErrs {
		if e != nil { out = append(out, e) }
	}
	return out
}

func (m *messageData) toUnified(raw []byte) fbshared.UnifiedMessage {
	um := fbshared.UnifiedMessage{
		Text:      m.BodyPreview,
		HTML:      m.BodyHtml,
		MsgFormat: fbshared.FmtText,
		Protocol:  fbshared.ProtoEmailImap,
		ChatID:    m.FolderID,
		ChatName:  m.FolderName,
		Username:  m.From,
		UserID:    m.From,
		MsgType:   fbshared.MsgTypeCreate,
		MsgID:     m.ID,
		Timestamp: time.Now().UnixNano(),
	}
	if t, err := time.Parse(time.RFC3339, m.ReceivedDateTime); err == nil {
		um.Timestamp = t.UnixNano()
	}
	um.Raw = raw
	return um
}
