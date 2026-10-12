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

const (
	maxMoreBatchesPerChan = 5
	seenKeepWindow        = 50000
)

var pubfn_ func(any) error
var muLounge sync.Mutex
var ircloungeClient *Client
var joinedMu sync.Mutex
var joinedSet = make(map[string]bool)
var seenMu sync.Mutex
var seenIDs = make(map[int]struct{})
var maxSeenID int64
var moreMu sync.Mutex
var morePages = make(map[int]int)
var lobbyMu sync.Mutex
var primaryLobbyID int
var netConnected bool
var pendingJoins []string

func SetPublishInfo(pubfn func(any) error) { pubfn_ = pubfn }

func publish(v any) error {
	if pubfn_ == nil {
		return nil
	}
	return pubfn_(v)
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
		moreMu.Lock()
		morePages = make(map[int]int)
		moreMu.Unlock()
		lobbyMu.Lock()
		primaryLobbyID = 0
		netConnected = false
		pendingJoins = nil
		lobbyMu.Unlock()
		client, err := Connect(server, user, password, maxSeen())
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
				var raw msgEventRaw
				if err := json.Unmarshal(event.Data, &raw); err != nil {
					log.Printf("irclounge: parse msg error: %v", err)
					pushError(err)
					continue
				}
				msg := raw.Msg
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
				emitLoungeMessage(msg, raw.Chan, event.Data)

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
					for i, net := range initData.Networks {
						log.Printf("irclounge: network=%s nick=%s connected=%v secure=%v",
							net.Name, net.Nick, net.Status.Connected, net.Status.Secure)
						if net.Name != requestedName {
							log.Printf("irclounge: WARNING: requested network name %q but server reports %q; settings (host/port/tls) may have been overridden (built-in name mapping or lockNetwork)", requestedName, net.Name)
						}
						if i == 0 || net.Name == requestedName {
							setPrimaryNetwork(net)
						}
						for _, ch := range net.Channels {
							log.Printf("irclounge:   channel %s (id=%d)", ch.Name, ch.ID)
							for _, m := range ch.Messages {
								emitLoungeMessage(m, ch.ID, synthMsgRaw(ch.ID, m))
							}
							if ch.TotalMessages > len(ch.Messages) && len(ch.Messages) > 0 {
								if err := client.RequestMore(ch.ID, ch.Messages[0].ID); err != nil {
									log.Printf("irclounge: request more error: %v", err)
									pushError(err)
								}
							}
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
								joinedMu.Lock()
								joinedSet[ch] = true
								joinedMu.Unlock()
								queueJoin(ch)
							}
						}
					}
					flushPendingJoins(client)
				}

			case "network:status":
				var st struct {
					Connected bool   `json:"connected"`
					Secure    bool   `json:"secure"`
					Network   string `json:"network"`
				}
				if err := json.Unmarshal(event.Data, &st); err != nil {
					log.Printf("irclounge: parse network:status error: %v", err)
					continue
				}
				lobbyMu.Lock()
				netConnected = st.Connected
				lobbyMu.Unlock()
				log.Printf("irclounge: network %s connected=%v secure=%v", st.Network, st.Connected, st.Secure)
				if st.Connected {
					flushPendingJoins(client)
				}

			case "network":
				var wrap struct {
					Network Network `json:"network"`
				}
				if err := json.Unmarshal(event.Data, &wrap); err == nil && wrap.Network.Name != "" {
					if wrap.Network.Name != requestedName {
						log.Printf("irclounge: WARNING: requested network name %q but server reports %q; settings (host/port/tls) may have been overridden", requestedName, wrap.Network.Name)
					}
				}

			case "network:name":
				log.Printf("irclounge: network name changed")

			case "more":
				var moreEv moreEvent
				if err := json.Unmarshal(event.Data, &moreEv); err != nil {
					log.Printf("irclounge: parse more error: %v", err)
					pushError(err)
					continue
				}
				for _, m := range moreEv.Messages {
					emitLoungeMessage(m, moreEv.Chan, synthMsgRaw(moreEv.Chan, m))
				}
				moreMu.Lock()
				pages := morePages[moreEv.Chan]
				moreMu.Unlock()
				if len(moreEv.Messages) == 100 && pages < maxMoreBatchesPerChan {
					moreMu.Lock()
					morePages[moreEv.Chan]++
					moreMu.Unlock()
					if err := client.RequestMore(moreEv.Chan, moreEv.Messages[0].ID); err != nil {
						log.Printf("irclounge: request more error: %v", err)
						pushError(err)
					}
				}

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

func Send(to, msg, msgType string, filedata []byte, _ *fbshared.MediaDataInfo, extra *fbshared.SendExtra) (fbshared.SendResult, error) {
	_ = extra
	if to == "" || msg == "" {
		return fbshared.SendResult{}, fmt.Errorf("irclounge: empty target or message")
	}
	muLounge.Lock()
	cl := ircloungeClient
	muLounge.Unlock()
	if cl == nil {
		return fbshared.SendResult{}, fmt.Errorf("irclounge: not connected")
	}
	channelID, err := strconv.Atoi(to)
	if err != nil {
		return fbshared.SendResult{}, fmt.Errorf("irclounge: invalid channel ID %q: %w", to, err)
	}
	return fbshared.SendResult{}, cl.SendMessage(channelID, msg)
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

func IsRunning() bool { return statusRunning.Load() }
func ConnectedSince() time.Time {
	v := statusConnectedSince.Load()
	if v == nil {
		return time.Time{}
	}
	return v.(time.Time)
}
func ReconnTimes() int64 { return statusReconnTimes.Load() }
func LastErrs() []error {
	statusLastErrsMu.Lock()
	defer statusLastErrsMu.Unlock()
	var out []error
	for _, e := range statusLastErrs {
		if e != nil {
			out = append(out, e)
		}
	}
	return out
}

func setPrimaryNetwork(net Network) {
	lobbyID := 0
	for _, ch := range net.Channels {
		if ch.Type == ChanLobby {
			lobbyID = ch.ID
			break
		}
	}
	if lobbyID == 0 && len(net.Channels) > 0 {
		lobbyID = net.Channels[0].ID
	}
	lobbyMu.Lock()
	if lobbyID != 0 {
		primaryLobbyID = lobbyID
	}
	if net.Status.Connected {
		netConnected = true
	}
	lobbyMu.Unlock()
}

func queueJoin(channel string) {
	lobbyMu.Lock()
	pendingJoins = append(pendingJoins, channel)
	lobbyMu.Unlock()
}

func flushPendingJoins(cl *Client) {
	lobbyMu.Lock()
	if !netConnected || primaryLobbyID == 0 || len(pendingJoins) == 0 {
		lobbyMu.Unlock()
		return
	}
	joins := pendingJoins
	pendingJoins = nil
	lobbyID := primaryLobbyID
	lobbyMu.Unlock()
	for _, ch := range joins {
		log.Printf("irclounge: joining channel %s", ch)
		if err := cl.Join(lobbyID, ch); err != nil {
			log.Printf("irclounge: join %s error: %v", ch, err)
			pushError(err)
		}
	}
}

func markSeen(id int) bool {
	seenMu.Lock()
	defer seenMu.Unlock()
	if _, ok := seenIDs[id]; ok {
		return false
	}
	seenIDs[id] = struct{}{}
	if int64(id) > maxSeenID {
		maxSeenID = int64(id)
	}
	if len(seenIDs) > seenKeepWindow {
		floor := maxSeenID - seenKeepWindow
		for k := range seenIDs {
			if int64(k) < floor {
				delete(seenIDs, k)
			}
		}
	}
	return true
}

func maxSeen() int64 {
	seenMu.Lock()
	defer seenMu.Unlock()
	return maxSeenID
}

func synthMsgRaw(chanID int, msg Message) []byte {
	b, err := json.Marshal(struct {
		Msg  Message `json:"msg"`
		Chan int     `json:"chan"`
	}{Msg: msg, Chan: chanID})
	if err != nil {
		return nil
	}
	return b
}

func emitLoungeMessage(msg Message, chanID int, rawEvent []byte) {
	if msg.Self {
		return
	}
	if msg.ID != 0 && !markSeen(msg.ID) {
		return
	}
	if rawEvent != nil {
		if err := publish(json.RawMessage(rawEvent)); err != nil {
			log.Printf("irclounge: publish error: %v", err)
			pushError(err)
		}
	}
	if um, ok := loungeMessageToUnified(msg, chanID, rawEvent); ok {
		if err := publish(um); err != nil {
			log.Printf("irclounge: publish error: %v", err)
			pushError(err)
		}
	}
}

func loungeUnifiedMsgType(t MessageType) string {
	switch t {
	case MsgAction:
		return "action"
	case MsgJoin:
		return fbshared.MsgTypeJoin
	case MsgPart, MsgQuit, MsgKick:
		return fbshared.MsgTypeLeave
	default:
		return fbshared.MsgTypeCreate
	}
}

func loungeMessageToUnified(msg Message, chanID int, raw []byte) (fbshared.UnifiedMessage, bool) {
	um := fbshared.UnifiedMessage{
		Protocol:  fbshared.ProtoIRCLounge,
		MsgType:   loungeUnifiedMsgType(msg.Type),
		MsgID:     msg.MsgID,
		MsgFormat: fbshared.FmtText,
		ChatID:    strconv.Itoa(chanID),
		Text:      msg.Text,
		Timestamp: time.Now().UnixNano(),
	}
	if msg.From != nil {
		um.Username = msg.From.Nick
		um.UserID = msg.From.Nick
	}
	if msg.Time != "" {
		if t, err := time.Parse(time.RFC3339, msg.Time); err == nil {
			um.Timestamp = t.UnixNano()
		}
	}
	if um.Text == "" && um.MsgType != fbshared.MsgTypeJoin && um.MsgType != fbshared.MsgTypeLeave {
		return um, false
	}
	um.Raw = raw
	return um, true
}
