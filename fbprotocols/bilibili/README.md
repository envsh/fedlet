# bilibili — 哔哩哔哩协议

fedlet 的 B 站接入协议(web 端 bilibili web api)。协议后端路径镜像 zhihu
(编排/认证/登录交互/热榜/通知一一对应)。

## 行为(第一阶段)
- **热榜**:读全局榜单(`GET /x/web-interface/ranking/v2?rid=0&type=all`),
  按 `bvid`(退化 `av<aid>`)去重,**仅发布新上榜条目**;首次轮询发布当前全榜,
  之后只发新增;**公开接口,无需登录**;轮询默认 600s。
- **关注动态**:读用户关注流(`GET /x/polymer/web-dynamic/v1/feed/all?type=all`),
  按 `id_str`(退化 `feed:<type>:<mid>`)去重,**仅发布新动态**;**需登录**;
  轮询默认 600s;首次轮询静默播种存量(不发布)。
- **通知事件**:未读数(`GET /x/msg/push-info/unread`)+ 评论/回复、@、赞三个列表
  (`GET /x/msg/reply|at|like`,type=1,page_size=20),未读数变化即发布聚合
  (`kind=notify_unread`),详情事件按 `type:id` 去重回填(`kind=notify_event`);
  **需登录**;轮询默认 60s;首次轮询静默播种。
- 去重状态持久化 `~/.config/fedlet/bilibili-state.json`(72h 过期收割)。

## 认证(二维码 + 密码 + 短信 + Cookie 四通道,对齐 bilibili web passport)
1. **二维码扫码**(主路径,官方 web 流):
   - `GET /x/passport-login/web/qrcode/generate` → `data.url` + `data.qrcode_key`;
   - 轮询 `GET /x/passport-login/web/qrcode/poll?qrcode_key=<key>`:`code`
     0 成功 / 86038 未扫 / 86090 已扫 / 86101 过期;成功响应的 Set-Cookie
     带回 `SESSDATA`/`bili_jct`/`DedeUserID`,body 带 `data.refresh_token`。
2. **密码登录**:`GET /x/passport-login/web/key` 取 `hash`+RSA PEM `key`,
   `hash+密码` 经 PKCS#1 v1.5 加密 + base64,POST `/x/passport-login/web/login`
   (`keep=true&source=main_web`);风控账号需极验 validate/challenge/seccode
   (页面提供粘贴入口)。
3. **手机号 + 验证码**:`GET /x/passport-login/web/sms/send`(cid=86,tel,source=main_web)
   → `GET /x/passport-login/web/sms/login`(cid/tel/code/keep)—— 端点布局按
   bilibili-API-collect,标 **待现场核实**。
4. **Cookie 粘贴登录**(无需扫码/密码,浏览器手动获取):
   - 获取路径:bilibili.com 登录后,F12 → Application → Cookies → `bilibili.com` →
     复制 `SESSDATA`(必填)、`bili_jct`(必填)、`DedeUserID`(可选)、
     `DedeUserID__ckMd5`(可选);
   - refresh_token:F12 → Console 执行 `localStorage.getItem('ac_time_value')`,
     复制返回值(**可选,但有它才能自动续期**);
   - 登录 UI 页面底部提供「Cookie 登录」输入框与获取说明;输入后经
     `GET /x/web-interface/nav` 校验即写入会话。
- 会话持久化 `~/.config/fedlet/bilibili-auth.json`(0600):`SESSDATA`/`bili_jct`/
  `DedeUserID`/`DedeUserID__ckMd5`/`refresh_token`/`user`/`status`;
  `AuthStatus()` = empty / ready / invalid;`login_method` 字段记录登录方式
  (**qr / password / sms / cookie**)。
- **过期检测**(主动 + 被动):主动 `probeSession`(nav 探测,登录成功 + 每 30 分钟);
  被动:登录态请求 JSON code `-101/-401` 或 HTTP 401 → 判 invalid;HTML 200/404/522
  → shed(保留会话);`-352/-412/-403` → 风控退避(保留会话)。
- **过期处理**:退出冷却 5 分钟后拉起登录 UI 重登;失败只入 `LastErrs()`。
- **单一会话入口**:所有拉取收敛到唯一 `ensureSession()`
  (empty→加载持久化→仍空→登录 UI;ready→放行,30 分钟探测翻转;invalid→refresh→UI),
  是唯一可能拉起登录 UI 的入口(`ui.active` 单实例守卫)。

## refresh_token 续期(单次调用,非完整 web 仪式)
- 采用 `POST /x/passport-login/web/refresh`(旧 SESSDATA cookie + form 旧
  refresh_token)一次性换新的 SESSDATA 族 + 新 refresh_token;**单飞**保证
  一对一轮换不并发竞态;新 pair **先落盘再作废旧 token**;随后
  `POST /x/passport-login/web/confirm/refresh` 尽力作废旧 token(失败非致命)。
- token 已死(-101/-111/86095)→ `ErrNotLoggedIn` → 登录 UI。
- **已知限制**:完整 web 仪式(取 cookie/info → RSA-OAEP correspondPath →
  刷新 → confirm)不实现,采用单次调用方案;若 B 站收紧单次调用,需按现场抓包
  回退完整仪式。

## 风控(shed 式,非交互)
- `-352` / HTTP 412 / 429 / HTTP 403 → `ErrRiskControl`,触发返回前记
  `noteRateLimit()` 退避(15s,轮询层 `ensureSession` 先行拦截),保留会话。
- HTML 200 / 404 / 522(反爬节点)→ `errUnverifiable`,整轮 shed,不触发登录。
- 短信/极验类人机验证按上文交互式方式处理(页面粘贴 validate/challenge/seccode)。

## 登录交互(自包含 HTTP server,镜像 zhihu)
- 随机端口、绑定 `127.0.0.1` 的**自包含** server,URL `http://127.0.0.1:<port>/`;
  页面内联单页(二维码自动启动 + 密码表单 + 短信表单 + Cookie 面板),JS 每
  1.5s 轮询 `/api/state`;二维码 180s 过期自动刷新。
- 自动打开浏览器(`xdg-open`/`open`/`cmd start`);登录成功(写 auth + 记录
  login_method)或超时 10 分钟 → `srv.Shutdown()` 用完即退;唯一实例保护。
- 无桌面环境(DISPLAY 为空)时 QR 降级提示改用 Cookie 登录。

## 推荐流(外部只调,不进轮询)
- `FetchRecommend(limit)`:WBI 签名拉取 `GET /x/web-interface/wbi/index/top/feed/rcmd`
  (`ps` + `fresh_type=4`,自动附带 `wts`/`w_rid`、SESSDATA 与 bili_ticket 尽力)。
  **独立 pull 层**:不发布、不去重、不触发登录网关;无会话即快速失败
  `ErrNotLoggedIn` 由调用方决定。

## 参数 / 来源(资料项目)与状态

| 参数 | 值 | 来源(资料项目) |
|---|---|---|
| WBI 签名 | img_key+sub_key 按 `mixinKeyEncTab` 64 位重排截 32;`w_rid=md5(sort(encodeURIComponent)+mixin)`;wts 时间戳;encodeURIComponent 大写转义 | bilibili-API-collect docs/misc/sign/wbi.md |
| 热榜 | `GET /x/web-interface/ranking/v2?rid=0&type=all&web_location=333.934` 公开 | bilibili-API-collect 排行接口 |
| 关注流 | `GET /x/polymer/web-dynamic/v1/feed/all?type=all&platform=web` 登录态 | bilibili-API-collect 动态(web)接口 |
| 推荐流 | `GET /x/web-interface/wbi/index/top/feed/rcmd`(WBI) | bilibili-API-collect 推荐接口(wbi) |
| 通知 | `GET /x/msg/push-info/unread`;`GET /x/msg/reply|at|like`(type=1) | bilibili-API-collect 消息中心接口 |
| 二维码 | generate / poll(qrcode_key, code 0/86038/86090/86101) | bilibili-API-collect 登录;poll 完成拿 refresh_token+Set-Cookie |
| 密码 | `web/key`(hash+PEM)→ `web/login`(geetest 可选) | bilibili-API-collect 登录/web 密码 |
| 短信 | `web/sms/send` / `web/sms/login`(cid=86) | bilibili-API-collect 登录/短信,**待现场核实** |
| refresh | `POST /x/passport-login/web/refresh` 单次调(非完整仪式);confirm/refresh 尽力 | bilibili-API-collect 刷新登录维持接口 |
| 会话探测 | `GET /x/web-interface/nav`(isLogin + uname + wbi_img 兼 WBI keys 来源) | bilibili-API-collect 登录信息 |

> 状态:热榜/关注流/推荐/二维码/密码主路径按 bilibili-API-collect 结构核验,
> 短信 send/login 与 msg/reply|at|like 的**响应字段标 待实测**(容错解析),
> 待真实 B 站会话联网后再回填。

## 文件
```
bilibili.go      协议编排:Start/Stop / ensureSession 网关 / 轮询循环 / 去重状态 / 退避 / 状态查询
client.go        共享 HTTP 客户端、cookie jar、buvid3 warmup、bili_ticket、错误分类
wbi.go           WBI 签名(mixinKeyEncTab / wts / w_rid / encodeURIComponent 大写)
auth.go          会话持久化(0600)、nav 探测、refresh_token 单飞轮换、AuthStatus
login.go         登录 UI(随机端口/127.0.0.1/QR+密码+短信+Cookie/用完即退)
loginpage.go     登录页内联 HTML/JS(Cookie 获取说明 + 粘贴表单)
hotboard.go      热榜(ranking/v2)拉取与发布
feed.go          关注动态(feed/all)拉取、字段提取、dedupe
notify.go        未读聚合 + reply/at/like 事件列表(容错解析)
recommend.go     FetchRecommend(limit) WBI 签名只拉层(不发布/不触登录网关)
bilibili_test.go 离线单测(WBI/round/dedupe/auth 持久化/网关/风控分类)
```

## 已知限制与下一步
- 通知字段、短信登录:需在真实会话下联网验证后回填;邮箱登录未实现。
- refresh 采用单次 `/refresh` 调用而非完整 web 仪式;若失效按抓包回退完整仪式。
- 热榜首轮发布当前全榜(约 100 条),避免冷启动空窗。
- README 红线:不直接运维运行中的 `./main`;测试用 `HOME=/tmp/opencode/zh-home`。