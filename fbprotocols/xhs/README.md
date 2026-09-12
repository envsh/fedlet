# xhs — 小红书协议

fedlet 的小红书接入协议(web 端 xhs.pc 协议 + xhshow 0.2.0 签名算法移植)。
协议后端路径镜像 zhihu(编排/认证/登录交互/热榜/通知一一对应)。

## 行为(第一阶段)
- **热榜**:读首页推荐接口的热榜分类(`POST /api/sns/web/v1/homefeed`,`category="homefeed.fashion_v3"`),
  按笔记 id(`note_card.id` 优先,退化用条目自身 `id`)去重,**仅发布新上榜条目**;轮询默认 600s。
  **游客会话即可**(`POST /api/sns/web/v1/login/activate` 拿的 web_session),无需真实登录。
- **通知事件**(评论/@、赞/收藏、新增关注):拉取三个登录态流
  (`GET /api/sns/web/v1/you/mentions|likes|connections`),按 `kind:id` 去重,**仅发布新事件**;
  轮询默认 60s。**需真实登录**。未读数 `GET /api/sns/web/unread_count`。
- 去重状态持久化 `~/.config/fedlet/xhs-state.json`(72h 过期收割)。

## 认证(二维码 + 手机验证码,双通道对齐 jackwener / PeanutSplash 客户端)
1. **二维码扫码**(官方 web 流,2026-09 双源核验):
   - `POST /api/sns/web/v1/login/qrcode/create` `{"qr_type":1}` → `qr_id`/`code`/`url`;
   - 轮询 `POST /api/qrcode/userinfo` `{"qrId","code"}`(头 `service-tag: webcn`):`codeStatus`
     0 未扫 / 1 已扫 / 2 已确认 / 3 过期;
   - 确认后 `GET /api/sns/web/v1/login/qrcode/status {qr_id,code}` 取会话
     (`data.session` 或 `login_info.session`)。
2. **手机号 + 验证码**:`GET /api/sns/web/v2/login/send_code`(phone/zone=86/type=login)
   → `GET /api/sns/web/v1/login/check_code` 换 `mobile_token`
   → `POST /api/sns/web/v2/login/code` 取 `web_session`。
3. **Cookie 粘贴登录**(无需扫码/短信,浏览器手动获取):
   - 获取路径:打开 xiaohongshu.com 登录后,F12 → Application → Cookies → `xiaohongshu.com` →
     复制 `a1`(必填)、`web_session`(必填)、`web_session_sec`(可选)、`webId`(可选)。
   - 登录 UI 页面底部提供「Cookie 登录」输入框与获取说明;输入后经 `/api/sns/web/v2/user/me`
     校验即写入会话。
   - 有效期有限,过期后需重新获取;有有效会话时可静默续期。
4. **游客唤醒**:`POST /api/sns/web/v1/login/activate {}` 拿游客 web_session(热榜前置)。
- 会话持久化 `~/.config/fedlet/xhs-auth.json`(0600),键为 cookie 名
  `WebSession`;`AuthStatus()` = empty / ready / invalid / guest;`login_method` 字段记录登录方式(qr/phone/cookie)。
- **过期检测**(双通道):主动 `verifySession`(`GET /api/sns/web/v2/user/me`,登录成功 + 每 30 分钟);
  被动:登录态请求 401/403/`-100 未登录` → 判 invalid。
- **过期处理**:通知停拉;`LastErrs()` 暴露;默认自动重登录(冷却 5 分钟、上限 2 次,
  然后仅日志提示);重登成功新 cookie 覆盖写盘恢复。
- **单一会话入口**:所有拉取收敛到唯一 `ensureSession()` 网关,是唯一可能拉起登录 UI
  的入口(`ui.active` 单实例守卫)。verify 瞬时错误只入 `LastErrs()`、不消耗重登预算不弹窗。

## 风控(461/471/472 验证码,交互式)
- 被拦响应带 `verifytype`/`verifyuuid` 头;客户端把挑战交给 `relayRisk`(客户端的
  `onRisk` 回调用例),同时**指数退避 4→8→16→20s**。
- `POST https://edith.xiaohongshu.com/api/redcaptcha/v2/captcha/register`
  (`captchaVersion 2.0.0, secretId 000`,payload 对照 ReaJason/xhs issue #93)拿 `rid` +
  `captchaInfo`(DES-ECB 加密,crypto/des 零新增依赖)。**register 请求也带
  `x-s`/`x-s-common` 签名头**(依 2024 技述文章;issue #93 示例未带,属保守增强,
  无 a1 时回退为普通头)→ **待实测**。
- **二次验证二维码**(verified from live traffic,urlscan):
  `https://www.xiaohongshu.com/web-login/qrcode-transfer?rid=…&verifyUuid=…&verifyBiz=<461|471>&verifyType=124&webid=…`,
  用小红书 App 扫码后点「已完成验证,继续」。rid 的官方初始化端点未核实 → **待实测**。
- **滑块路径**:解密 captchaInfo 拿到原图在登录页展示,页面完成 + 点继续;自动求解刻意不做。
- 风控面板与登录页共用同一单页,轮询 `/api/state` 合并展示。

## 登录交互(自包含 HTTP server,镜像 zhihu)
- 随机端口、绑定 `127.0.0.1` 的**自包含** server,URL `http://127.0.0.1:<port>/`;
  页面内联单页(二维码展示 + 手机号/验证码表单 + 风控面板),JS 每 1.5s 轮询 `/api/state`。
- 自动打开浏览器(`xdg-open`/`open`/`cmd start`);登录成功(写 auth)或超时 10 分钟 →
  `srv.Shutdown()` 用完即退;唯一实例保护。

## 签名(xhshow 0.2.0 Go 直译)
- `x-s`:XYS_/XYW_ 双模式;`x-s-common`:`a1;f=XP-xxxx;r=a1` 段;`x-t`、`x-b3-traceid`、
  `x-xray-traceid`、`x-mns: unload`、`xy-direction`(shardingKey)。
- `x-rap-param`:热榜等接口带 `withXRap`(SM4/xxh32/gzip,`xrap.go`)。
- Cookie 头固定位 + `referer`/`origin` 固定 web 根;POST 带 `x-org-version`、`x-client-via`。
- 数据表与流程对 `xhshow`(`test/public_api.py`)交叉验证,`golden_test.go`/
  `xpos_test.go` 有确定性/黄金向量/跨实现用例(a1/webId、shardingKey、CRC32、XYS/XYW、
  x-s-common、x-rap)。
- 请求头顺序约定:`Cookie` → `x-t`+`x-b3-traceid` → `x-s`/`x-s-common` → `xy-direction`。
- **x-s-common 与现网浏览器的差异**(待实测):浏览器 `s0=3 / x4=6.2.0`、约 328 字符(xhshow
  注释中 300/328 两种);本实现(xhshow 0.2.0)**约 1292 字符**、`s0=5 / x4=4.86.0`,
  `b1` 为合成指纹(无真实浏览器设备信息,xhshow 同款限制)——有一定概率被判定为
  非真实浏览器而触发 v2 风控;若账号/网络下 461 高发,优先怀疑此项。

## 参数 / 来源(资料项目)与状态

| 参数 | 值 | 来源(资料项目) |
|---|---|---|
| 热榜端点 | `POST /api/sns/web/v1/homefeed` `category=homefeed.fashion_v3`(guest 会话即可) | jackwener/xiaohongshu-cli `get_hot_feed`(2026-06);payload 逐字段对齐 |
| 热榜字段 | `data.items[].note_card.display_title` / `interact_info`(赞/藏/评)/ `user`;链接 `explore/<note_id>` | jackwener 客户端解析 + 容忍回退;待真实会话回填 |
| 通知端点 | `GET /api/sns/web/unread_count`;`GET /api/sns/web/v1/you/mentions\|likes\|connections`(num,cursor) | jackwener `get_notification_*`(2026-06);字段 待实测回填(容错) |
| 身份校验 | `GET /api/sns/web/v2/user/me` | jackwener `get_user_me`;guest=true 判游客 |
| 二维码创建 | `POST /api/sns/web/v1/login/qrcode/create {"qr_type":1}` | jackwener jw_qr_login.py + PeanutSplash pc_login_apis.py 双源 |
| 二维码轮询 | `POST /api/qrcode/userinfo {"qrId","code"}` codeStatus 0/1/2/3 | 同上 |
| 二维码完成 | `GET /api/sns/web/v1/login/qrcode/status {qr_id,code}` 取 session | jackwener + PeanutSplash(双源) |
| 手机验证码 | `GET /api/sns/web/v2/login/send_code` / `v1/login/check_code`(→mobile_token) / `v2/login/code` | PeanutSplash 手机登录流;待实测回填 |
| 游客唤醒 | `POST /api/sns/web/v1/login/activate {}` | jackwener `login_activate` |
| 风控状态 | 461/471/472;`verifytype`/`verifyuuid` 头;redcaptcha v2 register(DES-ECB) | ReaJason/xhs issue #93;二次验证 URL 由 urlscan 实测流量实证;rid 初始化 待实测 |
| 签名基线 | xhshow 0.2.0(XYS_/XYW_/x-s-common/x-rap);CRC32/webId/shardingKey 黄金向量 | xhshow(chinekingb/xhshow)ts test + public_api.py;`xpos_test.go` 跨实现核对 |

> 状态:面板/热榜/通知/登录的**成功响应结构大多经活跃维护参考项目核验,个别字段仍标 待实测**,
> 待真实 xhs 会话联网后再回填。

## 文件
```
xhs.go          协议编排:pollLoop / ensureSession 网关 / 去重状态 / throttle / 状态查询
client.go       共享 HTTP 客户端、cookie jar、签名头注入、461 风险拦截
sign.go         XYS_/XYW_/x-s-common 签名(xhshow 直译)
fingerprint.go  a1/webId/CRC32/shardingKey
xrap.go         x-rap-param(SM4/xxh32/gzip)
auth.go         登录 API(二维码 + 手机验证码)、会话持久化、verifySession
captcha.go      风控:redcaptcha register / 二次验证二维码 / DES 解密滑块图
loginsrv.go     自包含登录 UI + 风控面板(随机端口 / 127.0.0.1 / openurl / 用完即退 / Cookie 粘贴登录)
hotlist.go      热榜(homefeed fashion_v3)拉取与字段提取
notify.go       通知(三 you/ 流)拉取
golden_test.go/xpos_test.go/xrap_test.go/xhs_test.go  离线单测
```

## 已知限制与下一步
- 通知需真实登录:无会话时自动拉起登录 UI,期间只发布热榜(游客)。
- 二维码完成/手机登录/通知字段、redcaptcha rid 初始化端点:需在真实会话下联网验证后回填。
- 签名若被 xhs 更新:以 xhshow / 新逆向为准更新常量表与 header 顺序。