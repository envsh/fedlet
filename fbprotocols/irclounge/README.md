# irclounge — The Lounge IRC 桥接协议

Go 实现的 [The Lounge](https://thelounge.chat/) 客户端协议，基于 Engine.IO v4 HTTP polling 和 Socket.IO v4，用于 fedlet 联邦消息桥。

## 架构分层

```
lounge.go       — 顶层循环 (pollLounge), 配置解析, SetPublishInfo
client.go       — Client {Events chan}, Connect(), auth 循环, readLoop, SendMessage
socketio.go     — Socket.IO v4 事件层 (SioSession), SendConnect/Emit/ReadEvent
engineio.go     — Engine.IO v4 HTTP polling 传输层 (EioSession), 握手/读写/心跳
types.go        — Message, Channel, Network, User, Event 等数据类型
```

## 协议细节

### Engine.IO v4 (engineio.go)

- **HTTP polling 传输**：GET 长轮询收包，POST 发包
- **握手**：`GET /socket.io/?EIO=4&transport=polling` 返回 `0{json}`，包含 sid / pingInterval / pingTimeout
- **包格式**：`<type><data>`，type 为单数字 0–6（0=open, 1=close, 2=ping, 3=pong, 4=message, 5=upgrade, 6=noop）
- **多包**：同一响应体中以 `\x1E` (0x1E, Record Separator) 分隔多个包
- **心跳**：收到 `2` (ping) 自动回复 `3` (pong)
- **核心坑**：一次 `poll()` 返回的响应可能包含多个包（如 `40{}^^42["ev1"]^^42["ev2"]`），**必须全部缓冲**逐一返回。`EioSession.buf []EioPacket` 解决此问题

### Socket.IO v4 (socketio.go)

- **事件包**：`42["event", data]` — 类型 `2` (EVENT)，负载为 JSON 数组 `[name, ...args]`
- **CONNECT**：`40` — 类型 `4`(Engine.IO message) + 类型 `0`(Socket.IO CONNECT)
- **命名空间**：`42/namespace,["event",...]` — 支持 `/` 前缀，解析时跳过 namespace 前缀到第一个 `,`
- `ReadEvent()` 循环内部跳过 Engine.IO 非 Message 包和 Socket.IO 非 Event 包（CONNECT `0` 自动跳过）

### Client (client.go)

**认证流程（auth 循环）：**

```
client.SendConnect() → EIO POST 40
                      ↓
ReadEvent() 循环 ←—— EIO GET 长轮询
    ↓                    ↑
  配置/推送/auth:start...
    ↓
public 模式（无 user/pass）：自动收到 auth:success
private 模式：auth:start → Emit("auth:perform", {user, password}) → auth:success / auth:failed
```

- public 模式：Connect(server, "", "") 即可
- `auth:success` 返回后启动 `readLoop` 协程，事件推入 `client.Events`
- 消息发送：`SendMessage(channelID, text)` → `Emit("input", {target, text})`

### irclounge 顶层 (lounge.go)

- `Start(info)` → goroutine `pollLounge` 循环
- 配置格式：JSON `{"server":"...", "user":"...", "password":"..."}` 或 `user:password` 格式，默认 server `http://localhost:9000`
- 断线自动重连（10s + 5s）

## 关键注意事项

| 问题 | 解决方案 |
|------|----------|
| Engine.IO poll 一次返回多包丢失 | `EioSession.buf` 缓冲，`ReadPacket` 优先消费缓冲 |
| HTTP/2 长轮询阻塞 | `TLSNextProto` 置空强制 HTTP/1.1，`DisableKeepAlives: true` |
| 第一个 GET 握手也用自定义 client | 同样使用 HTTP/1.1 client，避免 DefaultTransport HTTP/2 残留 |
| CONNECT 包（`40`）必须客户端主动发送 | Server 不会发 `40`，不发送则无事件 |

## 测试

```
go test -v -timeout 60s ./fbprotocols/irclounge/
```

- 单元测试 20 项（JSON 编解码、包编码/解码、SIO 事件解析、配置解析、枚举常量）
- 集成测试 2 项（需联网）：
  - `TestDialEio`：握手 + pingInterval 验证
  - `TestFullConnect`：完整连接 demo.thelounge.chat，验证收到 init 事件
- 集成测试用 `demo.thelounge.chat`（public 模式），无需凭据

## 待办

- 注册 `fedbridge/` 入口（`//go:build irclounge` + `init()` 追加到 `starters`）
- private 模式认证集成测试（需本地 Lounge 实例）
- 消息类型扩展解析（join/part/quit/nick/topic/channel:state/names 等）
