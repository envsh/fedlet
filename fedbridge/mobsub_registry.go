package main

import (
	"encoding/json"
	"log"
	"reflect"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
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

// extractPubsubData 从 p2put.OnEvent 的 raw 里取出「消息字节 + 发布者 peer」:
//   - gossip 路径:topicListener 投递 *pubsub.Message(pso_pubsub.go:202)
//   - 转发/nopubsub 路径:p2put.Event{Type:"pubsub", Value:pubsubEvent{...}}
//     pubsubEvent 未导出,只能反射读 Data/From 字段。
func extractPubsubData(raw any) (data []byte, from string) {
	switch v := raw.(type) {
	case *pubsub.Message:
		if v == nil {
			return nil, ""
		}
		return v.Data, peer.ID(v.GetFrom()).ShortString()
	case pubsub.Message:
		return v.Data, peer.ID(v.GetFrom()).ShortString()
	}

	rv := reflect.ValueOf(raw)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil, ""
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, ""
	}
	if f := rv.FieldByName("Type"); f.IsValid() {
		if t, ok := f.Interface().(string); !ok || t != "pubsub" {
			return nil, ""
		}
	}
	val := rv.FieldByName("Value")
	for val.Kind() == reflect.Ptr || val.Kind() == reflect.Interface {
		if val.IsNil() {
			return nil, ""
		}
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return nil, ""
	}
	if f := val.FieldByName("Data"); f.IsValid() {
		if s, ok := f.Interface().(string); ok {
			data = []byte(s)
		}
	}
	if f := val.FieldByName("From"); f.IsValid() {
		from, _ = f.Interface().(string)
	}
	return data, from
}

// onP2PTopicEvent 消费 p2put 收到的消息:过滤 proto_type=mobsub,
// 解析 endpoint/p256dh/auth,按 auth 为键存入 map 并刷新 last_seen。
// 回调在 p2put 的独立 goroutine 里跑(fireCallbacks),故全程加锁。
func onP2PTopicEvent(raw any) {
	data, from := extractPubsubData(raw)
	if len(data) == 0 {
		return
	}
	var m struct {
		ProtoType string `json:"proto_type"`
		Endpoint  string `json:"endpoint"`
		P256dh    string `json:"p256dh"`
		Auth      string `json:"auth"`
		UAID      string `json:"uaid"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	if m.ProtoType != "mobsub" {
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
