# fbprotocols/matrixlite — Matrix 精简客户端后端

用原生 Matrix Client-Server API 拉取房间消息并统一为 `UnifiedMessage` 发布,
不依赖 mautrix/gomuks 库。

| 项 | 值 |
| --- | --- |
| build tag | `matrixlite` |
| 注册文件 | `fedbridge/matrixlite.go` |
| flag | `-matrix-url`、`-matrix-auth`(无 sliding 开关,自动探测) |
| 状态文件 | `~/.config/fedlet/matrixlite-state.json` |
| proto_type | `matrixlite`(`fbshared.ProtoMatrixLite`) |

## 同步路径

`Client.Sync`(client.go:286)按 `useSliding` 二选一:

| 路径 | 实现 | 端点 | 说明 |
| --- | --- | --- | --- |
| 滑动同步 | `slidingSync`(client.go:334) | `slidingSyncEndpoint`(client.go:20) | 来自 MSC3575 → 简化为 MSC4186;请求体只有 `lists`,**不请求任何 extensions** → receipts/typing/account_data 一律不下发 |
| v2 `/sync` | `normalSync`(client.go:451) | `/_matrix/client/v3/sync` | 兜底;滑动同步报错时回退(client.go:289-290) |

`useSliding` 由 `detectSlidingSync`(client.go:218)读 homeserver `/versions`
的 `unstable_features["org.matrix.simplified_msc3575"]` 自动决定。

## 事件过滤(2026-10)

本项目只使用 `m.room.message` 与 `presence`(以及 state 中的 room/member
元数据),**不解析 ephemeral / account_data**。故在 v2 filter(client.go:462)
显式排除:

```json
"ephemeral":{"not_types":["m.typing","m.receipt"]},
"account_data":{"not_types":["m.read","m.fully_read"]}
```

- `m.typing` / `m.receipt`:房间 ephemeral 事件(输入中 / 已读回执)。
- `m.read` / `m.fully_read`:房间 account_data(公开已读回执类型 / 已读标记)。
- MSC2285 私有回执 `m.read.private` 经 `m.receipt` 回传,已被整体排除覆盖,无需单列。

滑动同步路径无需改动(本就未请求这些 extension)。

## 为什么过滤:占比证据

- MSC4186 动机原文:老 `/sync` 使 clients **"cannot opt-out of a large amount
  of extraneous data such as receipts"**;sync v3 要解决 **"vast quantities of
  data (e.g read receipts for every room you're in)"**。
- Synapse 实测(50 真实用户 × 1 周)请求计数:

  | 请求 | 次数 |
  | --- | --- |
  | `PUT /rooms/typing`(×2 版本) | 10117 + 5904 = 16021 |
  | `POST /rooms/read_markers`(×2) | 5596 + 5554 = 11150 |
  | `POST /rooms/receipt/m.read` | 4038 |
  | `PUT /rooms/send`(真正发消息) | 3144 + 2048 = 5192 |

  → 输入中/已读类请求数 ≈ 消息发送数的 **6 倍**,这些在 `/sync` 中即对应
  每房间的 ephemeral + account_data,反复随每次同步下发。

## 与 gomuks 后端的对照

`fbprotocols/gomuks` 走的是 gomuks 服务的 websocket,其 `sync_complete` 内嵌
`rooms[*].receipts` 与 `account_data`。gomuks 无配置单独禁用这些,桥接端也不重编码
原始 envelope,故 `m.receipt`/`m.read`/`m.fully_read` 会随 `sync_complete` 原样上链;
仅 `typing` 是独立后端事件,可在桥接端丢弃。详见
`fbprotocols/gomuks/gomuks_websocket.go` 中忽略 `typing` 的注释。

## 参考

- MSC4186 Simplified Sliding Sync:
  https://github.com/matrix-org/matrix-spec-proposals/blob/main/proposals/4186-simplified-sliding-sync.md
- MSC3575 Sliding Sync(已 closed/obsolete,未并入 `main` 的 `proposals/`,故用 PR 链接):
  https://github.com/matrix-org/matrix-spec-proposals/pull/3575
- MSC2285 Private read receipts:
  https://github.com/matrix-org/matrix-spec-proposals/blob/main/proposals/2285-hidden-read-receipts.md
- MSC3079 Low-bandwidth Client-Server API(仍 open,未并入 `main`,故用 PR 链接):
  https://github.com/matrix-org/matrix-spec-proposals/pull/3079
- Synapse 可观测性/性能研究(SBRC):
  https://sol.sbc.org.br/index.php/sbrc/article/download/42353/42120/
