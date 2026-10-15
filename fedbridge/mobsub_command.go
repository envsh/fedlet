package main

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/gorilla/websocket"
)

// mobsub 只做接收订阅:Mozilla Push(autopush-rs),ntfy 主题=uaid。
// 收到消息仅记日志,不走 publish()。无状态文件、不做 register;uaid/endpoint
// 由 mozpush-reguaid.sh 预注册后经参数传入。密钥由 uaid 经 HKDF 确定性派生
// (uaid 即「密码」),不落盘;启动打印 p256dh/auth 供外部发送方加密。
//
// 注意:mozpush(WebPush)做不到广播——一个 endpoint 只对应一个 Application/uaid
// 连接;同 uaid 多连接会互相顶替(get_or_create_user 覆盖 node_id、registry 对新连接
// ghost 旧连接,本文件 close-1000 注释亦印证)。故多节点用「相同」moz 参数订阅同一
// uaid 不可能都收到;要「多节点都收到」只能各节点独立 uaid + 发送方 fan-out。

var ntfyTopicRe = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)

var mobsubUAIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func mobsubValidUAID(s string) bool { return mobsubUAIDRe.MatchString(s) }

// mobsubSameUAID 去横线 + 大小写无关,兼容服务端旧版 dashed / 现版 undashed 的 uaid 回显。
func mobsubSameUAID(a, b string) bool {
	return strings.EqualFold(strings.ReplaceAll(a, "-", ""), strings.ReplaceAll(b, "-", ""))
}

const (
	mobsubDefaultMozServer = "wss://push.services.mozilla.com/"
	mobsubSelfSendPeriod   = 39 * time.Second

	mobsubMinBackoff   = 5 * time.Second
	mobsubMaxBackoff   = 5 * time.Minute
	mobsubStableConn   = time.Minute
	mobsubReadTimeout  = 10 * time.Minute
	mobsubWriteTimeout = 10 * time.Second
)

// mobsubWSByHost 已知 Mozilla 部署的 endpoint host → WS host 映射
// (autopush 里 endpoint_hostname 与 WS hostname 是两项独立配置,无字符串推导规则)。
var mobsubWSByHost = map[string]string{
	"updates.push.services.mozilla.com": "wss://push.services.mozilla.com/",
	"updates-autopush.dev.mozaws.net":   "wss://autoconnect.dev.mozaws.net/",
	"updates-autopush.stage.mozaws.net": "wss://autoconnect.stage.mozaws.net/",
}

// mobsubWSServer 由 endpoint 决定 WS 地址(优先级):
//  1. endpoint host 命中已知 Mozilla 部署表 → 用表里的 WS;
//  2. 否则视为自建,按同源推导 ws(s)://<host[:port]>/;
//  3. endpoint 为空/非法 → 默认生产。
func mobsubWSServer(endpoint string) string {
	u, err := url.Parse(endpoint)
	if endpoint == "" || err != nil || u.Host == "" {
		return mobsubDefaultMozServer
	}
	if ws, ok := mobsubWSByHost[strings.ToLower(u.Hostname())]; ok {
		return ws
	}
	scheme := "ws"
	if u.Scheme == "https" {
		scheme = "wss"
	}
	return scheme + "://" + u.Host + "/"
}

// mobsubHTTPClient 无超时,供 ntfy /json 长连接使用。
var mobsubHTTPClient = &http.Client{}

type mobsubConfig struct {
	uaid     string
	endpoint string
	ntfyBase string
	server   string
}

type mobsubState struct {
	UAID       string
	ECPrivate  string
	ECPublic   string
	AuthSecret string
}

func buildMobSubConfig(ntfyBase, uaid, endpoint string) mobsubConfig {
	return mobsubConfig{
		uaid:     uaid,
		endpoint: endpoint,
		ntfyBase: ntfyBase,
		server:   mobsubWSServer(endpoint),
	}
}

func startMobSub(cfg mobsubConfig) {
	ntfyStatus := "on"
	ntfyOK := ntfyTopicRe.MatchString(cfg.uaid)
	if !ntfyOK {
		ntfyStatus = "off(invalid-topic)"
	}
	log.Printf("[mobsub] uaid=%s ntfy=%s endpoint=%s ws=%s", cfg.uaid, ntfyStatus, cfg.endpoint, cfg.server)
	if ntfyOK {
		go runMobSubNtfy(cfg)
	} else {
		log.Printf("[mobsub] uaid %q invalid for ntfy, skip ntfy", cfg.uaid)
	}
	st, err := deriveMobSubState(cfg.uaid)
	if err != nil {
		log.Printf("[mobsub] moz %s: init state: %v", cfg.uaid, err)
		return
	}
	log.Printf("[mobsub] moz %s: 分发用 p256dh=%s auth=%s", cfg.uaid, st.ECPublic, toDisplaySecret(st.AuthSecret))
	go runMobSubMozilla(cfg, st)
	if cfg.endpoint != "" {
		go runMobSubSelfSend(cfg, st)
	} else {
		log.Printf("[mobsub] moz %s: 无 -mozpush-endpoint,自发送关闭", cfg.uaid)
	}
}

func handleMobSubMsg(src string, msg []byte) {
	log.Printf("mobsub[%s]: %s", src, msg)
	err := publishBytes(channel_name, msg)
	if err != nil {
		log.Println("[mobsub] repub", err)
	}
}

// toDisplaySecret 日志打码:保留首尾各 3 位,中间以 *** 代替。
func toDisplaySecret(s string) string {
	if len(s) <= 6 {
		return "***"
	}
	return s[:3] + "***" + s[len(s)-3:]
}

// ---- ntfy ----

func runMobSubNtfy(cfg mobsubConfig) {
	for {
		if err := mobsubNtfyOnce(cfg); err != nil {
			log.Printf("[mobsub] ntfy %s: %v (retry in 10s)", cfg.uaid, err)
		}
		time.Sleep(10 * time.Second)
	}
}

func mobsubNtfyOnce(cfg mobsubConfig) error {
	u := strings.TrimRight(cfg.ntfyBase, "/") + "/" + cfg.uaid + "/json"
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
	log.Printf("[mobsub] ntfy %s: subscribed %s", cfg.uaid, u)
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
			log.Printf("[mobsub] ntfy %s: bad line: %v", cfg.uaid, err)
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
	backoff := mobsubMinBackoff
	for {
		start := time.Now()
		err := mobsubMozillaOnce(cfg, st)
		lived := time.Since(start)
		if lived >= mobsubStableConn {
			backoff = mobsubMinBackoff
		}
		if err != nil {
			log.Printf("[mobsub] moz %s: %v (稳定 %v,退避 %v)", cfg.uaid, err, lived.Truncate(time.Second), backoff)
		}
		time.Sleep(backoff)
		if backoff < mobsubMaxBackoff {
			backoff *= 2
		}
	}
}

func mobsubMozillaOnce(cfg mobsubConfig, st *mobsubState) error {
	conn, _, err := websocket.DefaultDialer.Dial(cfg.server, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	// 服务端每 5 分钟发一次 WebSocket Ping 控制帧;ReadMessage 只处理数据帧,
	// 控制帧内部回 pong 但不返回。故必须用 PingHandler 刷新读超时(收到 Ping 即
	// 证明对端存活),否则空闲(确实无消息)时会被读超时误判为断线。客户端无需自
	// 己发 Ping(服务端 Ping 已足够保活)。
	conn.SetReadDeadline(time.Now().Add(mobsubReadTimeout))
	conn.SetPingHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(mobsubReadTimeout))
		if werr := conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(mobsubWriteTimeout)); werr != nil {
			if werr == websocket.ErrCloseSent {
				return nil
			}
			if ne, ok := werr.(net.Error); ok && ne.Temporary() {
				return nil
			}
			return werr
		}
		return nil
	})
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(mobsubReadTimeout))
		return nil
	})

	hello := map[string]any{
		"messageType": "hello",
		"uaid":        st.UAID,
	}
	if err := conn.WriteJSON(hello); err != nil {
		return err
	}

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %s", mobsubCloseReason(err))
		}
		conn.SetReadDeadline(time.Now().Add(mobsubReadTimeout))
		if string(raw) == "{}" {
			if werr := conn.WriteMessage(websocket.TextMessage, []byte("{}")); werr != nil {
				return fmt.Errorf("write ping-reply: %w", werr)
			}
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("[mobsub] moz %s: bad frame: %v", cfg.uaid, err)
			continue
		}
		mt, _ := msg["messageType"].(string)
		status := mobsubStatus(msg["status"])
		switch mt {
		case "hello":
			if status != 200 {
				log.Printf("[mobsub] moz %s: hello rejected status=%d reason=%v", cfg.uaid, status, msg["reason"])
				return fmt.Errorf("hello status %d reason=%v", status, msg["reason"])
			}
			if u, _ := msg["uaid"].(string); u != "" && !mobsubSameUAID(u, st.UAID) {
				log.Printf("[mobsub] moz %s: WARN server uaid=%s != configured uaid=%s (请重新注册)", cfg.uaid, u, st.UAID)
			}
			log.Printf("[mobsub] moz %s: hello ok uaid=%s", cfg.uaid, st.UAID)
		case "notification":
			chid, _ := msg["channelID"].(string)
			version, _ := msg["version"].(string)
			dataB64, _ := msg["data"].(string)
			headers, _ := msg["headers"].(map[string]any)
			if dataB64 != "" {
				if pt, derr := decryptMozillaWebPush(dataB64, headers, st); derr != nil {
					log.Printf("[mobsub] moz %s: decrypt failed chid=%s version=%s: %v", cfg.uaid, chid, version, derr)
				} else {
					handleMobSubMsg("mozpush", pt)
				}
			}
			if werr := conn.WriteJSON(map[string]any{
				"messageType": "ack",
				"updates": []map[string]string{
					{"channelID": chid, "version": version},
				},
			}); werr != nil {
				return fmt.Errorf("write ack: %w", werr)
			}
		default:
			log.Printf("[mobsub] moz %s: unexpected frame %s status=%d reason=%v", cfg.uaid, mt, status, msg["reason"])
		}
	}
}

// ---- 自发送回环(验证订阅链路;密钥全内存生成,不落盘) ----

func runMobSubSelfSend(cfg mobsubConfig, st *mobsubState) {
	vpriv, vpub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		log.Printf("[mobsub] moz %s: self-send: 生成 VAPID 失败: %v", cfg.uaid, err)
		return
	}
	sub := &webpush.Subscription{
		Endpoint: cfg.endpoint,
		Keys: webpush.Keys{
			P256dh: st.ECPublic,
			Auth:   st.AuthSecret,
		},
	}
	opts := &webpush.Options{
		Subscriber:      "mobsub@localhost",
		VAPIDPublicKey:  vpub,
		VAPIDPrivateKey: vpriv,
		TTL:             39,
		HTTPClient:      httpClient30s,
	}
	var n uint64
	ticker := time.NewTicker(mobsubSelfSendPeriod)
	defer ticker.Stop()
	for range ticker.C {
		n++
		payload, err := json.Marshal(map[string]any{
			"proto_type":    "mobsub",
			"uaid":    "", // st.UAID,
			"virtip4": virtIP(),
			"peerid7": peerID7(),
			"seq":       n,
			"ts":      time.Now().Unix(),
		})
		if err != nil {
			log.Printf("[mobsub] moz %s: self-send: marshal: %v", cfg.uaid, err)
			continue
		}
		if err := sendMobSubSelf(sub, opts, payload); err != nil {
			log.Printf("[mobsub] moz %s: self-send #%d: %v", cfg.uaid, n, err)
			continue
		}
		log.Printf("[mobsub] moz %s: self-send #%d ok", cfg.uaid, n)
	}
}

// sendMozPushSunup 用订阅密钥(endpoint/auth/p256dh)按 RFC8291(aes128gcm)加密并发送一条
// WebPush(RFC8030)通知,可被 Android 上的 Sunup(UnifiedPush distributor,后端 Mozilla
// Autopush)解密。p256dh/auth 须来自 Sunup 订阅(UnifiedPush 注册所得),非本进程 uaid 派生值。
//
// 本实现走 v1 签名:endpoint 为注册时未绑定 VAPID 的 capability URL(UnifiedPush connector
// 默认 vapid=null,/wpush/v1/... ),故发送端 VAPID 可选、每次临时生成即可;Sunup APK
// (Autopush 后端,要求 Content-Encoding=aes128gcm + TTL 头、返回 201)与之兼容,可正常解密。
// 若订阅注册时绑定了 VAPID 公钥(v2 endpoint),则须配对私钥,本签名不覆盖(会 401)。
//
// 规范:UnifiedPush ANDROID 3.0(server→server = WebPush)、RFC8291 加密、RFC8292 VAPID。
func sendMozPushSunup(msg any, endpoint, auth, p256dh string) error {
	if endpoint == "" || auth == "" || p256dh == "" {
		return errors.New("sendMozPushSunup: endpoint/auth/p256dh 不可为空")
	}
	var payload []byte
	switch m := msg.(type) {
	case []byte:
		payload = m
	case json.RawMessage:
		payload = m
	case string:
		payload = []byte(m)
	case nil:
		payload = []byte("null")
	default:
		b, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("sendMozPushSunup: marshal: %w", err)
		}
		payload = b
	}
	vpriv, vpub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return fmt.Errorf("sendMozPushSunup: 生成 VAPID: %w", err)
	}
	sub := &webpush.Subscription{
		Endpoint: endpoint,
		Keys:     webpush.Keys{P256dh: p256dh, Auth: auth},
	}
	opts := &webpush.Options{
		Subscriber:      "mobsub@localhost", // → mailto:... 合法 URI
		VAPIDPublicKey:  vpub,
		VAPIDPrivateKey: vpriv,
		TTL:             60,
		HTTPClient:      httpClient30s,
	}
	return sendMobSubSelf(sub, opts, payload) // 复用:POST + 2xx(含 201)校验 + 关 body
}

func sendMobSubSelf(sub *webpush.Subscription, opts *webpush.Options, payload []byte) error {
	resp, err := webpush.SendNotification(payload, sub, opts)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func mobsubStatus(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

// mobsubCloseReason 区分 WebSocket 关闭原因,便于日志定位(uaid 被 reset/节点排空/网络断)。
func mobsubCloseReason(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Sprintf("read timeout(>%v 无任何帧,判定半开连接)", mobsubReadTimeout)
	}
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

var mobsubKDFSalt = []byte("fedlet/mobsub/v1")

// deriveMobSubState 由 uaid(=密码)经 HKDF-SHA256 确定性派生订阅密钥:
// P-256 私钥(p256dh)与 16B auth。同 uaid 必得同密钥,重启不变,不落盘。
func deriveMobSubState(uaid string) (*mobsubState, error) {
	secret := []byte(uaid)
	var priv *ecdh.PrivateKey
	for i := 0; i < 1000; i++ {
		raw, err := hkdf.Key(sha256.New, secret, mobsubKDFSalt, fmt.Sprintf("p256dh/%d", i), 32)
		if err != nil {
			return nil, err
		}
		if p, perr := ecdh.P256().NewPrivateKey(raw); perr == nil {
			priv = p
			break
		}
	}
	if priv == nil {
		return nil, errors.New("derive p256dh: no valid scalar")
	}
	auth, err := hkdf.Key(sha256.New, secret, mobsubKDFSalt, "auth", 16)
	if err != nil {
		return nil, err
	}
	return &mobsubState{
		UAID:       uaid,
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
