package main

import (
	"encoding/json"
	"testing"
	"time"

	p2put "github.com/envsh/libp2px/p2put"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
)

func resetMobsubRegistry() {
	mobsubSubsMu.Lock()
	mobsubSubs = map[string]*mobsubPushSub{}
	mobsubSubsMu.Unlock()
}

// subMsg 造一条走 gossip 路径的 *pubsub.Message。
func subMsg(payload any) *pubsub.Message {
	data, _ := json.Marshal(payload)
	topic := channel_name
	return &pubsub.Message{Message: &pb.Message{Data: data, Topic: &topic}}
}

func TestMobsubRegistryFilterAndUpsert(t *testing.T) {
	resetMobsubRegistry()

	// 非 mobsub → 忽略
	onP2PTopicEvent(subMsg(map[string]any{"proto_type": "weibo_hot", "endpoint": "x", "p256dh": "p", "auth": "a"}))
	// 是 mobsub 但缺 auth → 忽略
	onP2PTopicEvent(subMsg(map[string]any{"proto_type": "mobsub", "endpoint": "e", "p256dh": "p"}))
	if got := mobsubSnapshot(); len(got) != 0 {
		t.Fatalf("不该收集: %+v", got)
	}

	// 首次收集
	onP2PTopicEvent(subMsg(map[string]any{
		"proto_type": "mobsub", "endpoint": "https://ep/1", "p256dh": "p1", "auth": "a1", "uaid": "u1",
	}))
	snap := mobsubSnapshot()
	if len(snap) != 1 || snap[0].Auth != "a1" || snap[0].Endpoint != "https://ep/1" || snap[0].UAID != "u1" {
		t.Fatalf("首次收集异常: %+v", snap)
	}
	first := snap[0].FirstSeen

	// 同 auth 再来:更新 endpoint/p256dh/last_seen,first_seen 不变,条目数仍 1
	time.Sleep(time.Millisecond)
	onP2PTopicEvent(subMsg(map[string]any{
		"proto_type": "mobsub", "endpoint": "https://ep/2", "p256dh": "p2", "auth": "a1",
	}))
	snap = mobsubSnapshot()
	if len(snap) != 1 {
		t.Fatalf("upsert 后条目数应为 1,实际 %d", len(snap))
	}
	if snap[0].Endpoint != "https://ep/2" || snap[0].P256dh != "p2" {
		t.Fatalf("upsert 未更新: %+v", snap[0])
	}
	if !snap[0].FirstSeen.Equal(first) {
		t.Fatalf("first_seen 不应变化")
	}
	if snap[0].LastSeen.Before(first) {
		t.Fatalf("last_seen 应被刷新")
	}
}

func TestMobsubRegistryLazyExpire(t *testing.T) {
	resetMobsubRegistry()
	old := time.Now().Add(-4 * mobsubRegistryTTL)
	mobsubSubsMu.Lock()
	mobsubSubs["old"] = &mobsubPushSub{Auth: "old", Endpoint: "e", P256dh: "p", FirstSeen: old, LastSeen: old}
	mobsubSubs["live"] = &mobsubPushSub{Auth: "live", Endpoint: "e", P256dh: "p", FirstSeen: time.Now(), LastSeen: time.Now()}
	mobsubSubsMu.Unlock()

	onP2PTopicEvent(subMsg(map[string]any{
		"proto_type": "mobsub", "endpoint": "e2", "p256dh": "p2", "auth": "new",
	}))

	got := map[string]bool{}
	for _, e := range mobsubSnapshot() {
		got[e.Auth] = true
	}
	if got["old"] {
		t.Fatalf("超 3 天条目应被惰性清理")
	}
	if !got["live"] || !got["new"] {
		t.Fatalf("未过期/新条目应保留: %+v", got)
	}
}

// TestMobsubRegistryEventFallback 覆盖转发路径:p2put.Event{Type:"pubsub", Value: p2put.PubsubEvent}。
func TestMobsubRegistryEventFallback(t *testing.T) {
	resetMobsubRegistry()
	data, _ := json.Marshal(map[string]any{
		"proto_type": "mobsub", "endpoint": "https://ep/3", "p256dh": "p3", "auth": "a3",
	})
	evt := p2put.Event{Type: "pubsub", Topic: channel_name, Value: p2put.PubsubEvent{From: "peerX", Data: string(data)}}
	onP2PTopicEvent(evt)

	snap := mobsubSnapshot()
	if len(snap) != 1 || snap[0].Auth != "a3" {
		t.Fatalf("Event 转发路径解析失败: %+v", snap)
	}
	if snap[0].PeerID != "peerX" {
		t.Fatalf("应记录发布者 peer: %+v", snap[0])
	}

	// 非 pubsub 的 Event 应被忽略
	onP2PTopicEvent(p2put.Event{Type: "bootready", Value: "x"})
	if snap := mobsubSnapshot(); len(snap) != 1 {
		t.Fatalf("非 pubsub Event 应忽略: %+v", snap)
	}
}
