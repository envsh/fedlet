package main

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// mobsub 只做接收订阅:ntfy.sh 与 Mozilla Push(autopush-rs)。
// 收到消息仅记日志,不走 publish()。名字原样使用,不规整。

var ntfyTopicRe = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)

var mobsubUAIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func mobsubValidUAID(s string) bool { return mobsubUAIDRe.MatchString(s) }

// mobsubHTTPClient 无超时,供 ntfy /json 长连接使用。
var mobsubHTTPClient = &http.Client{}

type mobsubConfig struct {
	name      string
	channelID string
	uaid      string
	ntfyBase  string
	mozServer string
}

type mobsubState struct {
	ChannelID    string
	UAID         string
	PushEndpoint string
	ECPrivate    string
	ECPublic     string
	AuthSecret   string
}

// mobsubNameToChannelID 由固定名字确定性导出规范小写 dashed UUID(MD5 原生 16 字节,不截取)。
func mobsubNameToChannelID(name string) string {
	h := md5.Sum([]byte(name))
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

func buildMobSubConfig(name, mozServer, ntfyBase, uaid string) mobsubConfig {
	return mobsubConfig{
		name:      name,
		channelID: mobsubNameToChannelID(name),
		uaid:      uaid,
		ntfyBase:  ntfyBase,
		mozServer: mozServer,
	}
}

func startMobSub(cfg mobsubConfig) {
	ntfyStatus := "on"
	ntfyOK := ntfyTopicRe.MatchString(cfg.name)
	if !ntfyOK {
		ntfyStatus = "off(invalid-name)"
	}
	log.Printf("[mobsub] name=%q channelID=%s ntfy=%s moz=%s", cfg.name, cfg.channelID, ntfyStatus, cfg.mozServer)
	if ntfyOK {
		go runMobSubNtfy(cfg)
	} else {
		log.Printf("[mobsub] name %q invalid for ntfy, skip ntfy", cfg.name)
	}
	st, err := newMobSubState(cfg)
	if err != nil {
		log.Printf("[mobsub] moz %s: init state: %v", cfg.name, err)
		return
	}
	go runMobSubMozilla(cfg, st)
}

func handleMobSubMsg(src string, msg []byte) {
	log.Printf("mobsub[%s]: %s", src, msg)
}

// ---- ntfy ----

func runMobSubNtfy(cfg mobsubConfig) {
	for {
		if err := mobsubNtfyOnce(cfg); err != nil {
			log.Printf("[mobsub] ntfy %s: %v (retry in 10s)", cfg.name, err)
		}
		time.Sleep(10 * time.Second)
	}
}

func mobsubNtfyOnce(cfg mobsubConfig) error {
	u := strings.TrimRight(cfg.ntfyBase, "/") + "/" + cfg.name + "/json"
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return err
	}
	resp, err := mobsubHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	log.Printf("[mobsub] ntfy %s: subscribed %s", cfg.name, u)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev struct {
			Event   string `json:"event"`
			Topic   string `json:"topic"`
			Message string `json:"message"`
			Title   string `json:"title"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			log.Printf("[mobsub] ntfy %s: bad line: %v", cfg.name, err)
			continue
		}
		if ev.Event == "message" {
			handleMobSubMsg("ntfy", []byte(ev.Message))
		}
	}
	return sc.Err()
}

// ---- Mozilla autopush (WebPush) ----

func runMobSubMozilla(cfg mobsubConfig, st *mobsubState) {
	backoff := 5 * time.Second
	for {
		if err := mobsubMozillaOnce(cfg, st); err != nil {
			log.Printf("[mobsub] moz %s: %v (retry in %v)", cfg.name, err, backoff)
			time.Sleep(backoff)
			if backoff < 5*time.Minute {
				backoff *= 2
			}
			continue
		}
		backoff = 5 * time.Second
	}
}

func mobsubMozillaOnce(cfg mobsubConfig, st *mobsubState) error {
	conn, _, err := websocket.DefaultDialer.Dial(cfg.mozServer, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	hello := map[string]any{
		"messageType": "hello",
		"channelIDs":  []string{cfg.channelID},
		"use_webpush": true,
	}
	if st.UAID != "" {
		hello["uaid"] = st.UAID
	}
	if err := conn.WriteJSON(hello); err != nil {
		return err
	}

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %s", mobsubCloseReason(err))
		}
		if string(raw) == "{}" {
			conn.WriteMessage(websocket.TextMessage, []byte("{}"))
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("[mobsub] moz %s: bad frame: %v", cfg.name, err)
			continue
		}
		mt, _ := msg["messageType"].(string)
		status := mobsubStatus(msg["status"])
		switch mt {
		case "hello":
			if status != 200 {
				log.Printf("[mobsub] moz %s: hello rejected status=%d reason=%v", cfg.name, status, msg["reason"])
				return fmt.Errorf("hello status %d reason=%v", status, msg["reason"])
			}
			if u, _ := msg["uaid"].(string); u != "" && u != st.UAID {
				log.Printf("[mobsub] moz %s: WARN server uaid=%s != configured uaid=%s", cfg.name, u, st.UAID)
			}
			log.Printf("[mobsub] moz %s: hello ok uaid=%s", cfg.name, st.UAID)
		case "notification":
			chid, _ := msg["channelID"].(string)
			version, _ := msg["version"].(string)
			dataB64, _ := msg["data"].(string)
			headers, _ := msg["headers"].(map[string]any)
			if chid == cfg.channelID && dataB64 != "" {
				if pt, derr := decryptMozillaWebPush(dataB64, headers, st); derr != nil {
					log.Printf("[mobsub] moz %s: decrypt failed chid=%s version=%s: %v", cfg.name, chid, version, derr)
					return fmt.Errorf("decrypt notification chid=%s version=%s: %w", chid, version, derr)
				} else {
					handleMobSubMsg("mozpush", pt)
				}
			}
			conn.WriteJSON(map[string]any{
				"messageType": "ack",
				"updates": []map[string]string{
					{"channelID": chid, "version": version},
				},
			})
		default:
			log.Printf("[mobsub] moz %s: unexpected frame %s status=%d reason=%v", cfg.name, mt, status, msg["reason"])
		}
	}
}

func mobsubStatus(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

// mobsubCloseReason 区分 WebSocket 关闭原因,便于日志定位(uaid 被 reset/节点排空/网络断)。
func mobsubCloseReason(err error) string {
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		switch ce.Code {
		case websocket.CloseNormalClosure:
			return fmt.Sprintf("close 1000 normal (服务端主动关闭;多为 uaid 被 reset/被新连接顶替): %q", ce.Text)
		case websocket.CloseGoingAway:
			return fmt.Sprintf("close 1001 going-away(节点排空/重启): %q", ce.Text)
		case websocket.CloseAbnormalClosure:
			return fmt.Sprintf("close 1006 abnormal(网络断开): %q", ce.Text)
		default:
			return fmt.Sprintf("close %d: %q", ce.Code, ce.Text)
		}
	}
	return err.Error()
}

// decryptMozillaWebPush 解密 RFC8291 aes128gcm 载荷(与 webpush-go 发送端互逆)。
func decryptMozillaWebPush(dataB64 string, headers map[string]any, st *mobsubState) ([]byte, error) {
	for k, v := range headers {
		if strings.EqualFold(k, "content-encoding") {
			if ce, _ := v.(string); ce != "" && ce != "aes128gcm" {
				return nil, fmt.Errorf("unsupported content-encoding %q", ce)
			}
		}
	}
	body, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(dataB64, "="))
	if err != nil {
		return nil, fmt.Errorf("base64: %w", err)
	}
	if len(body) < 21 {
		return nil, fmt.Errorf("body too short (%d)", len(body))
	}
	salt := body[0:16]
	idlen := int(body[20])
	if len(body) < 21+idlen {
		return nil, fmt.Errorf("body too short for keyid")
	}
	keyid := body[21 : 21+idlen]
	ct := body[21+idlen:]

	priv, err := mobsubLoadPrivateKey(st)
	if err != nil {
		return nil, err
	}
	asPub, err := ecdh.P256().NewPublicKey(keyid)
	if err != nil {
		return nil, fmt.Errorf("server pubkey: %w", err)
	}
	secret, err := priv.ECDH(asPub)
	if err != nil {
		return nil, fmt.Errorf("ecdh: %w", err)
	}
	authSecret, err := base64.RawURLEncoding.DecodeString(st.AuthSecret)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}

	info := "WebPush: info\x00" + string(priv.PublicKey().Bytes()) + string(keyid)
	ikm, err := hkdf.Key(sha256.New, secret, authSecret, info, 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("aead: %w", err)
	}
	i := len(pt) - 1
	for i >= 0 && pt[i] == 0x00 {
		i--
	}
	if i >= 0 && pt[i] == 0x02 {
		pt = pt[:i]
	}
	return pt, nil
}

func newMobSubState(cfg mobsubConfig) (*mobsubState, error) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		return nil, err
	}
	return &mobsubState{
		ChannelID:  cfg.channelID,
		UAID:       cfg.uaid,
		ECPrivate:  base64.RawURLEncoding.EncodeToString(priv.Bytes()),
		ECPublic:   base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		AuthSecret: base64.RawURLEncoding.EncodeToString(auth),
	}, nil
}

func mobsubLoadPrivateKey(st *mobsubState) (*ecdh.PrivateKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(st.ECPrivate)
	if err != nil {
		return nil, err
	}
	return ecdh.P256().NewPrivateKey(raw)
}
