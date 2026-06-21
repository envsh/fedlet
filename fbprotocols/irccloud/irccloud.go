package irccloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func publish(data []byte) error {
	if pubfn_ == nil {
		return fmt.Errorf("pubfn not set")
	}
	return pubfn_(data)
}

var (
	pubfn_  func([]byte) error
	muIrc   sync.Mutex
	ircClient    *Client
	ircSessionKey string
)

func SetPublishInfo(pubfn func([]byte) error) {
	pubfn_ = pubfn
}

type ServerCfg struct {
	Hostname string   `json:"hostname"`
	Port     int      `json:"port"`
	SSL      bool     `json:"ssl"`
	Netname  string   `json:"netname"`
	Nickname string   `json:"nickname"`
	Realname string   `json:"realname"`
	Channels []string `json:"channels"`
}

type AppConfig struct {
	Email    string      `json:"email"`
	Password string      `json:"password"`
	Servers  []ServerCfg `json:"servers"`
	Channels []string    `json:"channels"`
}

func parseAppConfig(info string) *AppConfig {
	cfg := &AppConfig{
		Email:    os.Getenv("IRC_USER"),
		Password: os.Getenv("IRC_PASS"),
	}
	if info == "" {
		return cfg
	}

	if info[0] == '{' {
		var ac AppConfig
		if err := json.Unmarshal([]byte(info), &ac); err == nil {
			if ac.Email != "" {
				cfg.Email = ac.Email
			}
			if ac.Password != "" {
				cfg.Password = ac.Password
			}
			if len(ac.Channels) > 0 {
				cfg.Channels = ac.Channels
			}
			return cfg
		}
	}

	if strings.Contains(info, ":") {
		parts := strings.SplitN(info, ":", 2)
		cfg.Email = parts[0]
		rest := parts[1]
		if idx := strings.Index(rest, ","); idx >= 0 {
			cfg.Password = rest[:idx]
			for _, ch := range strings.Split(rest[idx+1:], ",") {
				ch = strings.TrimSpace(ch)
				if ch != "" {
					cfg.Channels = append(cfg.Channels, ch)
				}
			}
		} else {
			cfg.Password = rest
		}
	}
	return cfg
}

func Start(info string) {
	go poll_irccloud(info)
}

type streamState struct {
	lastEID        uint64
	streamID       string
	sessionKey     string
	cidMap         map[int]bool
	serverMap      map[string]int
	pendingJoins   map[int][]string
	serversChecked bool
}

func (st *streamState) processMessage(msg []byte, cl *Client, cfg *AppConfig) {
	var h struct {
		Type string `json:"type"`
		EID  uint64 `json:"eid"`
	}
	json.Unmarshal(msg, &h)

	if h.EID > st.lastEID {
		st.lastEID = h.EID
	}

	switch h.Type {
	case "header":
		var hdr struct {
			StreamID string `json:"streamid"`
		}
		json.Unmarshal(msg, &hdr)
		st.streamID = hdr.StreamID
		st.cidMap = make(map[int]bool)
		st.serversChecked = false

	case "makeserver":
		var srv struct {
			CID      int    `json:"cid"`
			Hostname string `json:"hostname"`
			Status   string `json:"status"`
		}
		json.Unmarshal(msg, &srv)
		log.Printf("makeserver: cid=%d hostname=%s status=%s", srv.CID, srv.Hostname, srv.Status)
		st.serverMap[srv.Hostname] = srv.CID
		st.checkServers(cl, cfg)
		connected := srv.Status == "connected_ready" || srv.Status == "connected" || srv.Status == "connected_joining"
		st.cidMap[srv.CID] = connected
		if connected {
			st.flushPendingJoins(cl)
		}

	case "status_changed":
		var sc struct {
			CID       int              `json:"cid"`
			NewStatus string           `json:"new_status"`
			FailInfo  *json.RawMessage `json:"fail_info"`
		}
		json.Unmarshal(msg, &sc)
		log.Printf("status_changed: cid=%d status=%s", sc.CID, sc.NewStatus)
		if sc.FailInfo != nil && len(*sc.FailInfo) > 2 {
			log.Printf("fail_info: %s", *sc.FailInfo)
		}
		st.checkServers(cl, cfg)
		connected := sc.NewStatus == "connected_ready" || sc.NewStatus == "connected" || sc.NewStatus == "connected_joining"
		st.cidMap[sc.CID] = connected
		if connected {
			st.flushPendingJoins(cl)
		}

	case "oob_include":
		var oob struct {
			URL string `json:"url"`
		}
		json.Unmarshal(msg, &oob)
		if oob.URL != "" {
			oobCh, err := cl.FetchOOB(st.sessionKey, oob.URL)
			if err != nil {
				log.Println("OOB fetch error:", err)
				st.streamID = ""
				st.lastEID = 0
			} else {
				for oobMsg := range oobCh {
					st.processMessage(oobMsg, cl, cfg)
					if err := publish(oobMsg); err != nil {
						log.Println("OOB publish error:", err)
					}
				}
			}
		}

	case "backlog_complete":
		log.Println("backlog complete, cids:", st.cidMap)
		log.Printf("cfg: servers=%d channels=%v", len(cfg.Servers), cfg.Channels)
		st.checkServers(cl, cfg)

	case "set_shard":
		var sh struct {
			APIHost string `json:"api_host"`
			Cookie  string `json:"cookie"`
		}
		json.Unmarshal(msg, &sh)
		if sh.Cookie != "" {
			st.sessionKey = sh.Cookie
		}
		if sh.APIHost != "" {
			log.Println("IRCCloud switching to shard:", sh.APIHost)
			cl.SetBaseURL(sh.APIHost)
		}

	case "idle":
		st.checkServers(cl, cfg)

	case "buffer_msg", "buffer_me_msg":
		var body struct {
			From string `json:"from"`
			Chan string `json:"chan"`
			To   string `json:"to"`
			Msg  string `json:"msg"`
		}
		if err := json.Unmarshal(msg, &body); err == nil {
			target := body.Chan
			if target == "" {
				target = body.To
			}
			log.Printf("<%s> <%s> %s", target, body.From, body.Msg)
		}

	case "channel_init":
		var ci struct {
			CID     int    `json:"cid"`
			Channel string `json:"chan"`
			Members []struct {
				Nick string `json:"nick"`
			} `json:"members"`
		}
		if err := json.Unmarshal(msg, &ci); err == nil {
			log.Printf("channel %s: %d users", ci.Channel, len(ci.Members))
		}
	}

	if err := publish(msg); err != nil {
		log.Println("publish error:", err)
	}
}

func (st *streamState) flushPendingJoins(cl *Client) {
	for cid, channels := range st.pendingJoins {
		if st.cidMap[cid] {
			for _, ch := range channels {
				log.Printf("joining %s on cid %d", ch, cid)
				if err := cl.Join(st.sessionKey, cid, ch, ""); err != nil {
					log.Println("join error:", err)
				}
			}
			delete(st.pendingJoins, cid)
		}
	}
}

func (st *streamState) checkServers(cl *Client, cfg *AppConfig) {
	if st.serversChecked {
		return
	}
	st.serversChecked = true

	if len(cfg.Servers) > 0 {
		for _, s := range cfg.Servers {
			if cid, ok := st.serverMap[s.Hostname]; ok {
				if connected, ok := st.cidMap[cid]; ok && connected {
					for _, ch := range s.Channels {
						cl.Join(st.sessionKey, cid, ch, "")
					}
				} else {
					st.pendingJoins[cid] = append(st.pendingJoins[cid], s.Channels...)
				}
			} else {
				netname := s.Netname
				if netname == "" {
					netname = s.Hostname
				}
				port := s.Port
				ssl := s.SSL
				if port == 0 {
					port = 6697
					ssl = true
				}
				nick := s.Nickname
				if nick == "" {
					nick = "fedlet"
				}
				real := s.Realname
				if real == "" {
					real = nick
				}
				cid, err := cl.AddServer(st.sessionKey, ServerParams{
					Hostname: s.Hostname,
					Port:     port,
					SSL:      ssl,
					Netname:  netname,
					Nickname: nick,
					Realname: real,
				})
				if err != nil {
					var sh *ShardRedirect
					if errors.As(err, &sh) {
						log.Println("add-server shard redirect:", sh.APIHost)
						cl.SetBaseURL(sh.APIHost)
						st.sessionKey = sh.Cookie
						cid, err = cl.AddServer(st.sessionKey, ServerParams{
							Hostname: s.Hostname,
							Port:     port,
							SSL:      ssl,
							Netname:  netname,
							Nickname: nick,
							Realname: real,
						})
						if err != nil {
							pushError(err)
							log.Println("add-server error after redirect:", err)
							continue
						}
					} else {
						pushError(err)
						log.Println("add-server error:", err)
						continue
					}
				}
				if err == nil {
					log.Printf("added server %s → cid %d", s.Hostname, cid)
					st.serverMap[s.Hostname] = cid
					if len(s.Channels) > 0 {
						st.pendingJoins[cid] = s.Channels
					}
				}
			}
		}
		return
	}

	if len(cfg.Channels) == 0 {
		return
	}

	for cid, connected := range st.cidMap {
		if connected {
			for _, ch := range cfg.Channels {
				cl.Join(st.sessionKey, cid, ch, "")
			}
			return
		}
	}

	addServer := func() (int, error) {
		return cl.AddServer(st.sessionKey, ServerParams{
			Hostname: "irc.freenode.net",
			Port:     6697,
			SSL:      true,
			Netname:  "Freenode",
			Nickname: "fedlet",
			Realname: "fedlet",
		})
	}
	cid, err := addServer()
	if err != nil {
		var sh *ShardRedirect
		if errors.As(err, &sh) {
			log.Println("add-server shard redirect:", sh.APIHost)
			cl.SetBaseURL(sh.APIHost)
			st.sessionKey = sh.Cookie
			cid, err = addServer()
			if err != nil {
				pushError(err)
				log.Println("add-server error after redirect:", err)
				return
			}
		} else if strings.Contains(err.Error(), "connecting_restricted") {
			log.Println("add-server restricted, falling back to add-default-server")
			cid, err = cl.AddDefaultServer(st.sessionKey, "fedlet", "fedlet")
			if err != nil {
				var sh2 *ShardRedirect
				if errors.As(err, &sh2) {
					log.Println("add-default-server shard redirect:", sh2.APIHost)
					cl.SetBaseURL(sh2.APIHost)
					st.sessionKey = sh2.Cookie
					cid, err = cl.AddDefaultServer(st.sessionKey, "fedlet", "fedlet")
					if err != nil {
						pushError(err)
						log.Println("add-default-server error after redirect:", err)
						return
					}
				} else {
					pushError(err)
					log.Println("add-default-server error:", err)
					return
				}
			}
		} else {
			pushError(err)
			log.Println("add-server error:", err)
			return
		}
	}
	log.Printf("added server irc.freenode.net → cid %d", cid)
	st.pendingJoins[cid] = cfg.Channels
}

func poll_irccloud(info string) {
	cfg := parseAppConfig(info)
	if cfg.Email == "" || cfg.Password == "" {
		log.Println("IRCCloud: IRC_USER/IRC_PASS not set")
		return
	}
	statusRunning.Store(true)
	statusConnectedSince.Store(time.Now())
	defer statusRunning.Store(false)

	cl := NewClient()
	muIrc.Lock()
	ircClient = cl
	muIrc.Unlock()
	state := &streamState{
		serverMap:    make(map[string]int),
		pendingJoins: make(map[int][]string),
	}

	for {
		if state.sessionKey == "" {
			t0 := time.Now()
			sk, err := cl.Authenticate(Config{Email: cfg.Email, Password: cfg.Password})
			if err != nil {
				pushError(err)
				log.Println("IRCCloud auth error:", err)
				time.Sleep(10 * time.Second)
				continue
			}
			state.sessionKey = sk
			muIrc.Lock()
			ircSessionKey = sk
			muIrc.Unlock()
			log.Println("IRCCloud authenticated in", time.Since(t0))
		}

		sinceID := ""
		if state.streamID != "" && state.lastEID > 0 {
			sinceID = fmt.Sprintf("%d", state.lastEID)
		}

		ch, err := cl.ConnectStream(state.sessionKey, sinceID, state.streamID)
		if err != nil {
			var sh *ShardRedirect
			if errors.As(err, &sh) {
				log.Println("IRCCloud shard redirect:", sh.APIHost)
				cl.SetBaseURL(sh.APIHost)
				state.sessionKey = sh.Cookie
				muIrc.Lock()
				ircSessionKey = sh.Cookie
				muIrc.Unlock()
				continue
			}
			pushError(err)
			log.Println("IRCCloud stream error:", err)
			state.sessionKey = ""
			muIrc.Lock()
			ircSessionKey = ""
			muIrc.Unlock()
			time.Sleep(5 * time.Second)
			continue
		}

		log.Println("IRCCloud stream connected")
		connStart := time.Now()
		for msg := range ch {
			state.processMessage(msg, cl, cfg)
		}
		log.Println("IRCCloud stream disconnected after", time.Since(connStart))
		time.Sleep(5 * time.Second)
	}
}

// protocol status
var (
	statusRunning        atomic.Bool
	statusConnectedSince atomic.Value // time.Time
	statusReconnTimes    atomic.Int64
	statusLastErrsMu     sync.Mutex
	statusLastErrs       [3]error
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

func Send(to, msg, msgType string) error {
	if to == "" || msg == "" {
		return fmt.Errorf("irccloud: empty to or message")
	}
	muIrc.Lock()
	cl := ircClient
	sk := ircSessionKey
	muIrc.Unlock()
	if cl == nil {
		return fmt.Errorf("irccloud: not connected")
	}
	if sk == "" {
		return fmt.Errorf("irccloud: no session key")
	}
	parts := strings.SplitN(to, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("irccloud: invalid target %q (need cid:channel)", to)
	}
	cid, err := strconv.Atoi(parts[0])
	if err != nil {
		return fmt.Errorf("irccloud: invalid cid %q: %w", parts[0], err)
	}
	return cl.Say(sk, cid, parts[1], msg)
}
