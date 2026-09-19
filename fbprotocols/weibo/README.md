# weibo — 微博协议

fedlet 的微博接入协议。**完全匿名**(免登录/免 cookie/免签名):一个裸 GET + JSON 解析 + 去重发布。

## 数据(master 端点一次返回三板块)
- **热搜榜**(`section:"realtime"`):`GET https://weibo.com/ajax/side/hotSearch`
  官方公开 JSON 端点,**2026-09-16 匿名实测通过**(`ok:1`,realtime ~52 条)。字段:
  `word` / `word_scheme`(#词#) / `note` / `num`(数字或字符串皆容错) / `rank`+`realpos` /
  `label_name`+`icon_desc`(新/热/沸/爆/商)。链接 `https://s.weibo.com/weibo?q=%23词%23&Refer=top`。
- **政务热搜**(`section:"hotgov"`):同响应 `data.hotgov`(单条 dict)+ `data.hotgovs`(列表),
  词自带 `#…#`;按 `section:word` 与主榜隔离去重。
- **文娱榜**(`section:"band"`):同响应 `data.band_list[]`,**部分时段才出现**,有则发、无则跳过。

统一 `kind:"weibo_hot"`,payload:`{kind, section, word, note, rank, label, hot, url, count, published_at}`;
轮询默认 **60s**;按 `section:word` 去重**仅发新上榜条目**。

## 状态
- 去重持久化 `~/.config/fedlet/weibo-state.json`(`hot` 段,72h 收割)。

## 签名 / 认证
- **无**。`AuthStatus()` 恒 `"public"`,`AuthUser()` 恒 `"weibo"`。
- 请求仅带浏览器 UA + `Referer: https://weibo.com/`(身份保护习惯),无 cookie/签名头。

## 备用(不接入代码)
- `https://v2.xxapi.cn/api/weibohot`:免费中转(**50 QPS、累计 8400万+ 次**,2026-09-16 本机实测
  可达);仅热搜主榜 50 条、无政务/文娱,字段为 `title/hot:"129万"/index`。仅作官方端点被收紧时的手动
  替代/人工备份记录,**本实现不依赖、不自动降级**。

## 待实测 / 风险
- 官方匿名端点可达性由微博决定,可随时收紧(需登录/访客 cookie) → 届时先从「备用」手动切换。
- 高频会触发风控:60s 轮询已属低频;失败仅入 `LastErrs()`、不打断对方。

## 文件
```
hotlist.go    热点端点与三板块解析(免认证;num 数字/字符串容错)
weibo.go      编排:Start / pollLoop / section:word 去重状态 / 状态面 / 匿名 HTTP 客户端
weibo_test.go 离线单测(解析 / 字段容错 / 三板块 round 发布计数与去重 / 错误面)
```

## 构建
```
cd fedbridge && go build -v -tags gomuks,toxoverhttp,outlookgraph,emailimap,zhihu,xhs,toutiao,weibo
```