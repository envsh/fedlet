package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	webpush "github.com/SherClockHolmes/webpush-go"
)

const webpushDefaultEndpoint = "https://updates.push.services.mozilla.com/wpush/v1/gAAAAABdemo"

var (
	webpushSub  *webpush.Subscription
	webpushOpts *webpush.Options
)

type webpushConfig struct {
	Endpoint     string       `json:"endpoint"`
	Keys         webpush.Keys `json:"keys"`
	VapidPublic  string       `json:"vapid_public"`
	VapidPrivate string       `json:"vapid_private"`
	Subscriber   string       `json:"subscriber"`
}

func webpushConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fedlet", "webpush.json")
}

func loadWebpushConfig() *webpushConfig {
	data, err := os.ReadFile(webpushConfigPath())
	if err != nil {
		return nil
	}
	var cfg webpushConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("webpush: parse config: %v", err)
		return nil
	}
	return &cfg
}

func saveWebpushConfig(cfg *webpushConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	p := webpushConfigPath()
	os.MkdirAll(filepath.Dir(p), 0700)
	return os.WriteFile(p, data, 0600)
}

func initWebpush() error {
	cfg := loadWebpushConfig()

	if cfg == nil {
		pub, priv, err := webpush.GenerateVAPIDKeys()
		if err != nil {
			return fmt.Errorf("webpush: 生成 VAPID 密钥失败: %v", err)
		}
		cfg = &webpushConfig{VapidPublic: pub, VapidPrivate: priv}
		if err := saveWebpushConfig(cfg); err != nil {
			return fmt.Errorf("webpush: 写入配置失败: %v", err)
		}
		return fmt.Errorf("webpush: 配置文件不存在，已生成 VAPID 密钥对，请填入 Subscription keys")
	}

	if cfg.VapidPublic == "" || cfg.VapidPrivate == "" {
		pub, priv, _ := webpush.GenerateVAPIDKeys()
		cfg.VapidPublic, cfg.VapidPrivate = pub, priv
		saveWebpushConfig(cfg)
		return fmt.Errorf("webpush: VAPID 密钥为空，已重新生成")
	}

	if cfg.Keys.P256dh == "" || cfg.Keys.Auth == "" {
		return fmt.Errorf("webpush: Subscription keys 为空，请从浏览器 Push API 获取后填入 %s", webpushConfigPath())
	}

	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = webpushDefaultEndpoint
	}
	webpushSub = &webpush.Subscription{
		Endpoint: endpoint,
		Keys:     cfg.Keys,
	}
	webpushOpts = &webpush.Options{
		Subscriber:      cfg.Subscriber,
		VAPIDPublicKey:  cfg.VapidPublic,
		VAPIDPrivateKey: cfg.VapidPrivate,
	}
	log.Println("webpush: 配置加载完成")
	return nil
}

// depppp
func publishWebPush(protocol, channel string, v any) error {
	if webpushSub == nil || webpushOpts == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	opts := *webpushOpts
	opts.Topic = ntfyshTopic
	opts.HTTPClient = httpClient31s
	resp, err := webpush.SendNotification(data, webpushSub, &opts)
	if err != nil {
		log.Printf("webpush: %v", err)
		return err
	}
	defer resp.Body.Close()
	return nil
}

func publishMozPush(protocol, channel string, v any) error {
	subs := mobsubSnapshot()
	if len(subs) == 0 {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	if matchPubMsg(data, "proto_type", "mobsub") {
		return nil
	}
	go func() {
		failn := 0
		var firstErr error
		for _, sub := range subs {
			if err := sendMozPushSunup(v, sub.Endpoint, sub.Auth, sub.P256dh); err != nil {
				failn++
				if firstErr == nil {
					firstErr = err
				}
				log.Printf("[mobsub] mozpush %s: auth=%s endpoint=%s: %v",
					protocol, toDisplaySecret(sub.Auth), sub.Endpoint, err)
				continue
			}
		}
		log.Printf("[mobsub] mozpush protocol=%s channel=%s ok=%d fail=%d firstErr=%v",
			protocol, channel, len(subs)-failn, failn, firstErr)
	}()
	return nil
}
