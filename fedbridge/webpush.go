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

func publishWebPush(protocol, channel string, v any) {
	if webpushSub == nil || webpushOpts == nil {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	opts := *webpushOpts
	opts.Topic = ntfyshTopic
	opts.HTTPClient = httpClient30s
	resp, err := webpush.SendNotification(data, webpushSub, &opts)
	if err != nil {
		log.Printf("webpush: %v", err)
		return
	}
	defer resp.Body.Close()
}
