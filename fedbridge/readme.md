read msgs from several backend and pub over libp2p

* step1 read toxhttpd rest
* step read gomuks websocket

seem's dont link p2put package, just use it's rest api is better.

peer disconnect very quick known.

### vtcp

https://github.com/KarpelesLab/pktkit/commit/c53a05d64c9b04f34473ebf55674566bbd357051

use this implment tun2proxy like but tun2peerid

### note drasil

drasil depend quic-go, conflict with libp2p's quick-go version

make sure libp2px/myvendor/go-libp2p used.

drasil's quic-go depend cannot remove

### todos

- [ ] protocols multiple instance support
- [ ] protocols config simple by json/yaml, less hard code build code integrate

### config

启动时按 `显式CLI > FEDLET_* 环境变量 > ~/.config/fedlet/config.json > flag默认` 合并,
经 `flag.Set` 回写既有 flag(见 `multi_source_config.go`)。键名即 flag 名,值写字符串。

- `GET /api/config`: 返回全部 flag 现值(敏感项打码)、default、source、applies(`hot`|`restart`)。
- `PATCH /api/config` (或 PUT): 请求体 `{"<flag>":"<值>"}`, 热应用并写入 `config.json`;
  返回 `updated/hot_applied/restart_required/unknown`; 有未知键返回 207;任一已知 flag 的
  值非法(如 duration 解析失败)则整单拒绝、返回 400(不落盘、不热应用),响应含 `invalid`。

#### 运行时热生效(无需重启)

`PATCH` 即时调用 `applyLive`:`flag.Set` 回写进程内 flag + copy-on-write 更新
`liveCfg`(`atomic.Pointer`)快照。已改造为"用时读取"的后端经注入 getter
(`liveGet`/`liveDuration`)在每个 tick / 每次使用时读取,故改动无需重启、无需
Stop/Start 协议即可生效(见 `multi_source_config.go:applyLive`)。

- `hot`:`sysinfo-interval`(tick 内 `Ticker.Reset`)、`misskey-timeline`;其余后端
  仍为 `restart`(已持久化,但需重启进程才完全生效)。
- 生效延迟上限:每 tick / 每轮读取,故 tick 型 ≤ 旧间隔、`misskey-timeline` ≤ 30s
  (poll 周期)才会用到新值。
- 连接态不热:`misskey`/`misskey-token` 更换后抓取请求立即用新凭据,但
  `VerifyToken`/`accountId` 是 Start 期状态,需重连才一致。故其 getter 虽为"用时
  读取",仍**未列入 `hot`**,`applies` 保守上报 `restart`。
