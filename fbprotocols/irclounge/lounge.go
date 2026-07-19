package irclounge

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

var pubfn_ func([]byte) error
var muLounge sync.Mutex
var ircloungeClient *Client
var joinedMu sync.Mutex
var joinedSet = make(map[string]bool)

func SetPublishInfo(pubfn func([]byte) error) { pubfn_ = pubfn }

func publish(data []byte) error {
	if pubfn_ == nil {
		return nil
	}
	return pubfn_(data)
}

func split2(s, sep string) []string {
	for i := 0; i < len(s)-len(sep); i++ {
		if s[i:i+len(sep)] == sep {
			return []string{s[:i], s[i+len(sep):]}
		}
	}
	return nil
}

func Start(server, auth, joinChannels, networkCfg string) {
	go pollLounge(server, auth, joinChannels, networkCfg)
}

func pollLounge(server, auth, joinChannels, networkCfg string) {
	user, password := "", ""
	statusRunning.Store(true)
	statusConnectedSince.Store(time.Now())
	defer statusRunning.Store(false)
	statusReconnTimes.Add(1)
	if auth != "" {
		parts := split2(auth, ":")
		if len(parts) >= 2 {
			user, password = parts[0], parts[1]
		}
	}
	log.Printf("irclounge: server=%s user=%s", server, user)

	defNet := DefaultNetwork()
	requestedName := defNet.Name
	requestedHost := defNet.Host
	requestedPort := defNet.Port
	if networkCfg != "" {
		var netCfg NetworkConfig
		if err := json.Unmarshal([]byte(networkCfg), &netCfg); err == nil {
			if netCfg.Name != "" {
				requestedName = netCfg.Name
			}
			if netCfg.Host != "" {
				requestedHost = netCfg.Host
			}
			requestedPort = netCfg.Port
		}
	}

	for {
		client, err := Connect(server, user, password)
		if err != nil {
			log.Printf("irclounge: connect error: %v", err)
			pushError(err)
			time.Sleep(10 * time.Second)
			continue
		}
		muLounge.Lock()
		ircloungeClient = client
		muLounge.Unlock()
		log.Println("irclounge: connected")

		for event := range client.Events {
			switch event.Type {
			case "msg":
				msg, parseErr := ParseMsgEvent(event.Data)
				if parseErr != nil {
					log.Printf("irclounge: parse msg error: %v", parseErr)
					pushError(parseErr)
				} else {
					from := ""
					if msg.From != nil {
						from = msg.From.Nick
					}
					log.Printf("irclounge: <%s> %s", from, msg.Text)
					if from == "" && strings.HasPrefix(msg.Text, "Network created, connecting to ") {
						rest := strings.TrimPrefix(msg.Text, "Network created, connecting to ")
						rest = strings.TrimRight(rest, ".")
						host, portStr, ok := strings.Cut(rest, ":")
						if ok {
							port, err := strconv.Atoi(strings.TrimRight(portStr, "."))
							if err == nil && (host != requestedHost || port != requestedPort) {
								log.Printf("irclounge: WARNING: server connected to %s:%d instead of requested %s:%d; settings may have been overridden (lockNetwork)", host, port, requestedHost, requestedPort)
							}
						}
					}
				}
			if err := publish(event.Data); err != nil {
				log.Printf("irclounge: publish error: %v", err)
				pushError(err)
			}
			um := loungeMsgToUnified(event.Data)
			umData, _ := json.Marshal(um)
			publish(umData)

			case "init":
				log.Println("irclounge: initial state loaded, parsing...")
				var initData InitData
				if err := json.Unmarshal(event.Data, &initData); err != nil {
					log.Printf("irclounge: init parse error: %v", err)
					pushError(err)
				} else if len(initData.Networks) == 0 {
					log.Println("irclounge: no networks configured")
					cfg := DefaultNetwork()
					if networkCfg != "" {
						if err := json.Unmarshal([]byte(networkCfg), &cfg); err != nil {
							log.Printf("irclounge: invalid network config: %v", err)
							pushError(err)
						}
					}
					if joinChannels != "" {
						if cfg.Join != "" {
							cfg.Join += ","
						}
						cfg.Join += joinChannels
					}
					log.Printf("irclounge: creating network %s (%s)", cfg.Name, cfg.Host)

					if err := client.CreateNetwork(cfg); err != nil {
						log.Printf("irclounge: create network error: %v", err)
						pushError(err)
					}
				} else {
					for _, net := range initData.Networks {
						log.Printf("irclounge: network=%s nick=%s connected=%v secure=%v",
							net.Name, net.Nick, net.Status.Connected, net.Status.Secure)
						if net.Name != requestedName {
							log.Printf("irclounge: WARNING: requested network name %q but server reports %q; settings (host/port/tls) may have been overridden (built-in name mapping or lockNetwork)", requestedName, net.Name)
						}
						for _, ch := range net.Channels {
							log.Printf("irclounge:   channel %s (id=%d)", ch.Name, ch.ID)
							joinedMu.Lock()
							joinedSet[ch.Name] = true
							joinedMu.Unlock()
						}
					}
					if joinChannels != "" {
						wanted := strings.Split(joinChannels, ",")
						for _, ch := range wanted {
							ch = strings.TrimSpace(ch)
							if ch == "" {
								continue
							}
							joinedMu.Lock()
							already := joinedSet[ch]
							joinedMu.Unlock()
							if !already {
								log.Printf("irclounge: joining channel %s", ch)
							if err := client.Join(ch); err != nil {
								log.Printf("irclounge: join %s error: %v", ch, err)
								pushError(err)
							}
								joinedMu.Lock()
								joinedSet[ch] = true
								joinedMu.Unlock()
							}
						}
					}
				}

			case "network:status", "network", "network:name":
				if event.Type == "network" {
					var wrap struct {
						Network Network `json:"network"`
					}
					if err := json.Unmarshal(event.Data, &wrap); err == nil && wrap.Network.Name != "" {
						if wrap.Network.Name != requestedName {
							log.Printf("irclounge: WARNING: requested network name %q but server reports %q; settings (host/port/tls) may have been overridden", requestedName, wrap.Network.Name)
						}
					}
				}

			case "join", "part", "quit", "nick", "topic":
				log.Printf("irclounge: %s %s", event.Type, string(event.Data))

			case "channel:state", "names", "users":
				log.Printf("irclounge: channel event %s", event.Type)

			default:
				log.Printf("irclounge: event %s", event.Type)
			}
		}

		muLounge.Lock()
		ircloungeClient = nil
		muLounge.Unlock()
		log.Println("irclounge: disconnected, reconnecting in 5s")
		client.Close()
		time.Sleep(5 * time.Second)
	}
}

func Send(to, msg, msgType string, filedata []byte, _ *fbshared.MediaDataInfo) error {
	if to == "" || msg == "" {
		return fmt.Errorf("irclounge: empty target or message")
	}
	muLounge.Lock()
	cl := ircloungeClient
	muLounge.Unlock()
	if cl == nil {
		return fmt.Errorf("irclounge: not connected")
	}
	channelID, err := strconv.Atoi(to)
	if err != nil {
		return fmt.Errorf("irclounge: invalid channel ID %q: %w", to, err)
	}
	return cl.SendMessage(channelID, msg)
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

func loungeMsgToUnified(data []byte) fbshared.UnifiedMessage {
	um := fbshared.UnifiedMessage{
		Protocol:  fbshared.ProtoIRCLounge,
		MsgType:   fbshared.MsgTypeCreate,
		Timestamp: time.Now().UnixNano(),
	}

	var raw struct {
		Msg  Message `json:"msg"`
		Chan int     `json:"chan"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return um
	}

	um.MsgID = raw.Msg.MsgID
	if raw.Msg.From != nil {
		um.UserName = raw.Msg.From.Nick
		um.UserID = raw.Msg.From.Nick
	}
	um.Text = raw.Msg.Text
	um.MsgFormat = fbshared.FmtText
	um.ChatID = strconv.Itoa(raw.Chan)

	if raw.Msg.Type == "action" {
		um.MsgType = "action"
	}

	if raw.Msg.Time != "" {
		if t, err := time.Parse(time.RFC3339, raw.Msg.Time); err == nil {
			um.Timestamp = t.UnixNano()
		}
	}

	um.Raw = data
	return um
}
