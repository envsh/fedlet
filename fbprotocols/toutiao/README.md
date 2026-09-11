# toutiao — 今日头条协议

fedlet 的今日头条接入协议。**双轨、全匿名**(免登录/免 cookie/免签名),是 fedlet 中最简单的
后端:一个裸 GET + JSON 解析 + 去重发布。

## 双轨
- **热榜**(`kind:"hotlist"`):`GET https://www.toutiao.com/hot-event/hot-board/?origin=toutiao_pc`
  官方 PC 公开接口,约 50 条;按 `ClusterIdStr` 去重**仅发新上榜条目**;轮询默认 600s
  (服务端缓存 ~5-10 分钟)。字段:`Title` / `HotValue`(字符串或数字皆容错)/ `Label`+`LabelDesc` /
  `InterestCategory[]` / 链接 `trending/{ClusterIdStr}/`。
- **实时新闻流**(`kind:"news"`):`GET https://www.toutiao.com/api/pc/feed/?category=__all__&count=20&max_behot_time=0`
  首页资讯流,按 `behot_time` 倒序;**增量水印** `last_behot` + `group_id` 去重,仅发新文章;
  过滤 `is_feed_ad` 广告;轮询默认 120s;详情链接 `article/{group_id}/`。

## 状态
- 去重 + 新闻水印持久化 `~/.config/fedlet/toutiao-state.json`(`hotlist`/`news` 两段,72h 收割)。

## 签名 / 认证
- **无**。`AuthStatus()` 恒 `"public"`,`AuthUser()` 恒 `"toutiao"`。
- 请求仅带浏览器 UA + `Referer: https://www.toutiao.com/`(身份保护习惯),无需 a1/webId/签名头。

## 待实测 / 风险
- 新闻流端侧可能在收紧:2026 多篇逆向文称 feed 需 `a_bogus/msToken/_signature` 与 `tt_webid`
  cookie;当前(2026-09-10 实测)匿名 `message:success` 直出,但接口可随时变更 → **待实测**。
  热榜端点为上网本接口、社区长期稳定使用,风险低。
- 高频会触发 429/503/空响应:循环节已按接口缓存设低频率;失败仅入 `LastErrs()`、不打断对方。
- `count` 实际返回条数可能少于请求值(实测 8 条/次,正常)。

## 文件
```
toutiao.go    编排:Start(hot, news) / pollLoop / 水印 / 去重状态 / 状态面 / 匿名 HTTP 客户端
hotlist.go    热榜端点与解析(免认证)
news.go       实时资讯流与页码水印(next.max_behot_time)
toutiao_test.go  离线 fixture 单测(解析 / 广告过滤 / 水印 / 状态往返)
```

## 构建
```
cd fedbridge && go build -v -tags gomuks,toxoverhttp,outlookgraph,emailimap,zhihu,xhs,toutiao
```