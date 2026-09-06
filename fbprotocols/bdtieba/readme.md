# Bdtieba 百度贴吧协议说明

无账号拉取指定吧的帖子列表，原始 JSON 推送。
当前仅实现 `fbprotocols/bdtieba/` 包，尚未集成到 fedbridge。

## 接口调研（2026-09 实测）

| 接口 | 免登录 | 反爬 / 限制 | 结论 |
|---|---|---|---|
| `/mg/f/getFrsData?kw=<吧名>` | ✅ | 无反爬，约 1 分钟 10 次 | ✅ 采用 |
| `/c/f/frs/page_claw` | ❌ | 需 TB_TOKEN（2026 已加反爬） | ❌ 弃用 |
| 批量聚合（如 `get_follow_forums`） | ❌ | 需 BDUSS 登录态 | 批量不可行 |

无匿名批量接口：帖子列表接口均为"一次请求一个吧"，必须循环拉取。

### kw 参数语义

`kw` 是**精确吧名（fname）**，不是关键字搜索：

- 会自动重定向到规范名：`kw=安卓` → `forum.name=android`
- 会自动去掉"吧"后缀：`kw=游戏吧` → `forum.name=游戏`
- 不存在的吧名返回空（`thread_list=0`、`forum=None`），不做模糊检索
- 返回的 `data.forum.name` / `data.forum.id` 是规范吧名与 fid

### 实测响应字段

`data.forum`（id/name）、`data.thread_list[]`：

| 字段 | 说明 |
|---|---|
| `tid` / `id` | 帖子 ID（全局唯一，排重键） |
| `title` | 标题 |
| `abstract` | `[{ "text": ... }]` 首帖摘要数组 |
| `reply_num` | 回复数 |
| `create_time` / `last_time_int` | unix 秒时间戳 |
| `is_top` | 1=置顶 |
| `author` | `{id, name, name_show, portrait, show_nickname}` |

`data.page.total_num` 表示主题帖总数。

## URL 拼接

- 新版：`https://tieba.baidu.com/p/{tid}`
- 旧版：`https://tieba.baidu.com/mo/q/m?kz={tid}`
- 触屏版：`https://waptieba.baidu.com/p/{tid}?lp=5028&mo_device=1`
- 吧首页：`https://tieba.baidu.com/f?kw=<url-enc 吧名>&ie=utf-8`

说明：网页端请求返回 403 是反爬（浏览器/APP 打开正常），JSON 接口不受影响。

## 拉取策略

- 循环拉取：每个 kw 单独 `FetchFrs`（rn=30, pn=1），无批量接口
- 间隔：请求间 60s、整轮 600s（周期已按需求扩大 10 倍）
- tid 排重 3 天窗口（内存 map + 持久化 `~/.config/fedlet/bdtieba-state.json`，>72h 清理）
- 跳过 `is_top=1` 置顶帖
- 推送格式：`{"forum": {"id","name"}, "thread": {原始字段}}`（非 UnifiedMessage）
- 新帖带日志输出

## 现成库调研（2026-09 搜索）

### Go 生态（无可用免登列表库，均老旧 / 面向登录）

| 库 | 年代 | 能力 | 结论 |
|---|---|---|---|
| `github.com/go-tgod/tgod` | ~2017 | `ThreadListRequest` 爬虫 | 老接口，未维护 |
| `github.com/pa001024/reflex/util/tieba` | 2016 | 登录/签到/GetFid | 需账号 |
| `github.com/Purstal/go-tieba-base` | 2015 | 基础操作 | 死库 |
| `github.com/iikira/baidu-tools/tieba` | 旧 | BDUSS 登录/签到 | 需账号 |
| `git.trustie.net/dupcs/du-tools/tieba` | 旧 | 签到/GetBars(by uid) | 需账号 |
| 其余签到/回帖机器人库 | 2013-2017 | — | 不适用 |

没有 Go 库提供无账号 `getFrsData` 首页帖子列表，也无批量多吧。

### 非 Go 生态（活跃维护，需登录能力时参考）

- **`aiotieba`**（Python，lumina37）：`get_threads(fname)` 免登读取，功能最全
- **`tieba.js`**（TypeScript/Node，Dilettante258）：`getThreads({fname,page,rn,sort})`，bduss 可空
- **`AioTieba4DotNet`**（C# 移植，BaWuZhuShou）
- **`Tieba-API-SCF`**（HTTP 门面，Bun/Node/CF Worker，需部署）
- 交叉验证：MediaCrawler 帖吧模块的 `note_url=https://tieba.baidu.com/p/{tid}`、`tieba_link=…kw=<enc>` 拼接与本仓库一致

## 结论

- Go 生态无现成免登列表库 → 自研 `bdtieba` 合理，且符合「服务端必须 Go」约束
- 当前需求（免登拉列表）无需外部 SDK；发帖/签到/楼中楼/搜索等登录级能力才需引入 aiotieba / tieba.js 或 sidecar

## 当前状态

- ✅ 已实现：`types.go`（结构体）、`client.go`（FetchFrs）、`bdtieba.go`（Start/pollLoop/排重/状态）、`urls.go`（URL 拼接）
- ⏳ 未集成：尚未注册进 fedbridge（无 build tag、无 flags、无 fbshared 常量）
- 📁 默认吧：`linux`、`android`、`个人电脑`、`游戏吧`、`gpt`、`ai人工智能`