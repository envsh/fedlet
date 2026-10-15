package main

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	p2put "github.com/envsh/libp2px/p2put"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/tidwall/gjson"
)

// mobsubRegistryTTL 订阅条目过期时限;惰性清理:每次 upsert 时删掉超时的。
const mobsubRegistryTTL = 72 * time.Hour

// mobsubPushSub 一条 UI 客户端的推送订阅(来自 proto_type=mobsub 的 p2put 消息)。
// auth 为 WebPush 订阅的 auth 密钥,天然是唯一键,故用作 map key。
type mobsubPushSub struct {
	Auth      string    `json:"auth"`
	Endpoint  string    `json:"endpoint"`
	P256dh    string    `json:"p256dh"`
	UAID      string    `json:"uaid,omitempty"`
	PeerID    string    `json:"peer_id,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

var (
	mobsubSubsMu sync.RWMutex
	mobsubSubs   = map[string]*mobsubPushSub{} // key = auth
)

// extractPubsubData 从 p2put.OnEvent 的 raw 里取出「消息字节 + 发布者 peer」。
// 只有两条路径承载 pubsub 数据,其余事件(libp2p 系统事件等)直接丢弃:
//   - gossip 路径:topicListener 投递 *pubsub.Message(pso_pubsub.go:202)
//   - 转发/nopubsub 路径:p2put.Event{Type:"pubsub", Value:p2put.PubsubEvent{...}}
//     直接类型断言读取 Data/From,无反射。
func extractPubsubData(raw any) (data []byte, from string) {
	switch v := raw.(type) {
	case *pubsub.Message:
		if v == nil {
			return nil, ""
		}
		return v.Data, peer.ID(v.GetFrom()).ShortString()
	case pubsub.Message:
		return v.Data, peer.ID(v.GetFrom()).ShortString()
	case p2put.Event:
		pe, ok := v.Value.(p2put.PubsubEvent)
		if !ok {
			return nil, ""
		}
		return []byte(pe.Data), pe.From
	default:
		return nil, ""
	}
}

// onP2PTopicEvent 消费 p2put 收到的消息:过滤 proto_type=mobsub,
// 解析 endpoint/p256dh/auth,按 auth 为键存入 map 并刷新 last_seen。
// 回调在 p2put 的独立 goroutine 里跑(fireCallbacks),故全程加锁。
// 通道内混着全部协议流量,故先用 gjson 精确判定 proto_type 作廉价过滤,
// 只对疑似 mobsub 的载荷做一次 Unmarshal。
func onP2PTopicEvent(raw any) {
	data, from := extractPubsubData(raw)
	if len(data) == 0 {
		return
	}
	// 第一步:按 key 精确判定(优于子串匹配,聊天内容里的 "mobsub" 不会误触发),
	// 缺失/非 mobsub 一律返回,不进入全量解码。
	if gjson.GetBytes(data, "proto_type").String() != "mobsub" {
		return
	}
	// 第二步:命中的少数才做一次 Unmarshal 取全部字段。
	var m struct {
		Endpoint string `json:"endpoint"`
		P256dh   string `json:"p256dh"`
		Auth     string `json:"auth"`
		UAID     string `json:"uaid"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	if m.Endpoint == "" || m.P256dh == "" || m.Auth == "" {
		return
	}

	now := time.Now()
	cutoff := now.Add(-mobsubRegistryTTL)
	mobsubSubsMu.Lock()
	defer mobsubSubsMu.Unlock()

	// 惰性过期清理(不新增 goroutine):趁这次写入顺手清掉超时条目。
	for k, e := range mobsubSubs {
		if e.LastSeen.Before(cutoff) {
			delete(mobsubSubs, k)
			log.Printf("[mobsub] 订阅过期清理 auth=%s", toDisplaySecret(k))
		}
	}

	if e, ok := mobsubSubs[m.Auth]; ok {
		e.Endpoint, e.P256dh, e.LastSeen = m.Endpoint, m.P256dh, now
		if m.UAID != "" {
			e.UAID = m.UAID
		}
		if from != "" {
			e.PeerID = from
		}
		return
	}
	mobsubSubs[m.Auth] = &mobsubPushSub{
		Auth:      m.Auth,
		Endpoint:  m.Endpoint,
		P256dh:    m.P256dh,
		UAID:      m.UAID,
		PeerID:    from,
		FirstSeen: now,
		LastSeen:  now,
	}
	log.Printf("[mobsub] 收集订阅 auth=%s endpoint=%s uaid=%s peer=%s",
		toDisplaySecret(m.Auth), m.Endpoint, m.UAID, from)
}

// mobsubSnapshot 供后续 push 到移动 app 取用:深拷贝,不泄露内部指针。
func mobsubSnapshot() []*mobsubPushSub {
	mobsubSubsMu.RLock()
	defer mobsubSubsMu.RUnlock()
	out := make([]*mobsubPushSub, 0, len(mobsubSubs))
	for _, e := range mobsubSubs {
		cp := *e
		out = append(out, &cp)
	}
	return out
}
