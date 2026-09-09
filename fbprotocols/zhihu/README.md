# zhihu — 知乎协议

fedlet 的知乎接入协议(参考 zhihu-plus / zhihu_sign_rs / pyzhihu-cli / ZhihuMCP 的实现思路)。

## 行为(第一阶段)
- **热榜**:拉取知乎热榜(`/api/v3/feed/topstory/hot-lists/total?limit=50&mobile=true`),按 feed id 去重,**仅发布新上榜条目**;轮询默认 600s。
- **通知事件**(被赞/评论/关注等):拉取登录态通知流(`/api/v3/notifications`),按 id 去重,**仅发布新事件**;轮询默认 60s。
- 去重状态持久化 `~/.config/fedlet/zhihu-state.json`(72h 过期收割)。
- **热榜与通知都需要登录态**。2026-09 联网实测:无 z_c0 的热榜请求(含预热 d_c0、浏览器 UA、HTTP/2、x-zse-96 签名)返回
  `{"error":{"code":101,"message":"身份未经过验证"}}`——恰与 pyzhihu-cli 等工具"热榜依赖登录"的现状一致。

## 认证(zhihu-plus 同款双通道,登录流对齐 zhihu-cli / zhihu-plus-plus)
1. **二维码扫码**:
   - `POST /api/v3/account/api/login/qrcode` 取 `token`+`link`(GET 返回 405);
   - 轮询 `GET /api/v3/account/api/login/qrcode/{token}/scan_info`:`status 0` 未扫、
     `status 1` 已扫待确认;成功凭 body 的 `access_token`/`user_id`、独体的
     `z_c0` 或 `cookie`/`cookies` 字段内的 `z_c0`(或 Set-Cookie `z_c0`)判定。
2. **手机号 + 验证码**:发送验证码后提交登录,换取 `z_c0`。
- 所有 POST 请求带 `X-Xsrftoken`(取自 `_xsrf`,缺失则省略)。
- 会话持久化 `~/.config/fedlet/zhihu-auth.json`(0600);状态 `AuthStatus()` =
  empty / ready / invalid。
- **过期检测**(双通道):
  - 主动:登录成功后校验一次,此后每 30 分钟用 `/api/v4/me` 轻量探测;
  - 被动:任何登录态请求 401 / `code 101` → 立即判过期;
  - 分流:403(风控)→ `throttle` 退避保留会话;401 → 判 invalid。
- **过期处理**:热榜/通知停拉;`LastErrs()` 暴露;默认自动重登录(冷却 5 分钟、
  最多 2 次,然后仅日志提示)。重登成功新 `z_c0` 覆盖写盘恢复运行。
- **单一会话入口**:热榜与通知共用主 `z_c0` 会话。所有拉取路径收敛到唯一的
  `ensureSession()` 网关——内部完成"ready→verify / 缺失或失效→登录 UI",
  是唯一可能拉起登录 UI 的入口(`ui.active` 单实例守卫保证浏览器至多打开一次)。
  verify 的**瞬时错误**(网络/5xx)只记入 `LastErrs()`、保持会话,不消耗重登
  预算也不弹窗;只有确认会话失效(401/101)才按冷却 5 分钟、上限 2 次触发。

## 登录交互(自包含 HTTP server)
- 会话缺失/失效时,协议内启动一个**自包含** HTTP server:随机端口,
  **绑定 `127.0.0.1`(不用 localhost,规避 ::1 双栈问题)**,URL 统一为
  `http://127.0.0.1:<随机端口>/`。
- 页面内联单页:二维码展示(JS 每 1.5s 轮询本服务 `GET /api/state`)+
  手机号/验证码表单(`POST /api/phone`、`POST /api/verify`)。
- **自动打开浏览器**(`xdg-open` / `open` / `cmd start`,按 GOOS 选择;失败仅打日志)。
- **用完即退**:登录成功(写 auth 文件)或超时 10 分钟 → `srv.Shutdown()` 自动退出,
  不留后台服务。
- 唯一实例保护:已有一个 UI 运行时再次触发返回既有 URL。

## 签名(参考实现移植)
`sign.go` 为 `zly2006/zhihu_sign_rs`(`src/lib.rs`)的**纯 Go 直译**,数据表与流程
逐字节照搬(常量 `ZK[32]`、S 盒 `ZB[256]`、`KEY16`、`ALPHABET`、seed=210;变换链
`g_transform`→`r_block`→`x_blocks`→`custom_encode`)。

请求头约定:
```
x-zse-93:           101_3_3.0
x-zse-96:           2.0_ + encrypt(md5("101_3_3.0+<path+query>+<d_c0>"))
x-requested-with:   fetch
```
来源:zhihu_sign_rs 基于 zhihu++(zly2006),二者均为 **AGPL-3.0**;README 与
`sign.go` 文件头已声明来源。

## 会话预热(warmup)
首次请求前 `sync.Once` 完成(游客 d_c0 与登录前签名共用):
- GET `https://www.zhihu.com/`:捕获 Set-Cookie(`_zap`、`_xsrf`;若下发 `d_c0`
  则覆盖使用)、从页面/响应头提取 `x-api-version`;
- `POST /udid`  `{}`:拿服务端下发的真 `q_c1`/`d_c0`(前端登录流靠它避免 101;
  拉取失败退回本地兜底 `q_c1`、生成 `d_c0`);
- `GET /api/v3/oauth/captcha?lang=cn`:拿 `capsion_ticket`(扫码/手机登录前置,
  纯轮询可忽略);
- 兜底生成 `q_c1`(前端 JS 维护的游客 cookie,服务端视为不透明 token)。

所有 warmup 步失败仅降级日志。签名里与 Cookie 中的 `d_c0` 恒一致(同一 `sess` 源)。

## 参数 / 来源(资料项目)

| 参数 | 值 | 来源(资料项目) |
|---|---|---|
| 热榜端点 | `GET /api/v3/feed/topstory/hot-lists/total?limit=50&mobile=true`(**需 z_c0**) | zhihu-plus-plus `HotListViewModel.kt`(同源签名);RSSHub PR #19075(`reverse_order=0`,需 ZHIHU_COOKIES);2026-09 实测匿名 101;待真实会话回填字段 |
| 热榜目标字段 | `target.title_area.text` / `excerpt_area.text` / `metrics_area.text` / `link.url`,旧结构回退 `target.title / detail_text / excerpt` | SnailDev/zhihu-hot-hub 实测结构;RSSHub hot.ts 旧结构对照 |
| 通知端点 | `GET /api/v3/notifications?limit=20&offset=0`(**需 z_c0**) | pyzhihu-cli 通知命令 + zhihu web;待实测回填 |
| 身份校验 | `GET /api/v4/me` | pyzhihu-cli「/api/v4/me 验证会话」;实测回填 |
| 二维码登录 | `POST /api/v3/account/api/login/qrcode`(GET→405)→ 轮询 `GET .../qrcode/{token}/scan_info`(`status` 0/1) | pyzhihu-cli(zxc67373/zhihu-cli `auth.py` 官方流,注释明言 GET 405);scan_info body 可携带 `cookie`/`cookies`/`z_c0`;待实测回填字段 |
| 手机验证码 | `/api/v3/account/api/send_verify_code`、`/api/v3/account/api/login` | zhihu web 登录流(zhihu-plus 同源);**待实测回填**(字段容错处理) |
| 反 CSRF | `X-Xsrftoken`(自 `_xsrf`,缺失省略) | pyzhihu-cli auth.py(避免 403) |
| 手机验证码 | `/api/v3/account/api/send_verify_code`、`/api/v3/account/api/login` | zhihu web 登录流(zhihu-plus 同源);**待实测回填**(字段容错处理) |
| 签名 | `x-zse-93` + `x-zse-96 = 2.0_+encrypt(md5(...))` | zhihu_sign_rs(Rust,基于 zhihu++ KMP),AGPL-3.0;本仓库 `sign_test.go` 有确定性/向量测试 |
| 匿名会话 | 预热 GET 首页 + `POST /udid` + `GET /api/v3/oauth/captcha?lang=cn` → `_zap`/`_xsrf`/`q_c1`/`d_c0`/`capsion_ticket`;各步兜底 | ZhihuMCP `PREWARM_HOME` 思路 + pyzhihu-cli auth.py 前置 cookie + 实测 101 判定 |
| 风控 | 403/`10003` → 指数退避 4→8→16→20s;401/101 → 会话失效重登 | 本项目 bdtieba throttle 模式 + ZhihuMCP 分层取数 |
| OAuth 否决 | 知乎无正式第三方 OAuth2(zhihu-oauth 用非正当 CLIENT_ID;openapi 为 app-token 型) | RainwXY/zhihu-oauth、developer.zhihu.com 官方说明 |
| 交互 | 自包含随机端口 HTTP server,`127.0.0.1`,自动 openurl,用完即退 | 本项目 outlookgraph 回环思路去依赖;用户指定 |

> 状态:登录/通知/热榜**成功响应结构待真实 z_c0 会话验证后回填**(表内已标 待实测)。

## 文件
```
zhihu.go        协议编排:pollLoop / ensureSession 网关 / 去重状态 / throttle / 状态查询
client.go       共享 HTTP 客户端、cookie 会话、getRaw、签名注入、预热
sign.go         zse-96 Go 移植(参考 zhihu_sign_rs, AGPL)
auth.go         登录 API(二维码 + 手机验证码)、会话持久化、过期检测
loginsrv.go     自包含登录 UI(随机端口 / 127.0.0.1 / openurl / 用完即退)
hotlist.go      热榜拉取与字段提取(title_area/excerpt_area/metrics_area/link)
notify.go       通知拉取
zhihu_test.go   离线单测(签名确定性/来源串、解析、去重、AuthStatus、登录门禁)
```

## 已知限制与下一步
- 热榜/通知需登录:无会话时自动拉起登录 UI,期间不发布。
- 登录成功路径与通知字段结构:需要在真实会话下联网验证后回填上表。
- 签名若被知乎更新:以 zhihu_sign_rs / zhihu++ 最新逆向为准更新常量表。