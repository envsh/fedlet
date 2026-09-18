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

## 图片图床分裂（2026-09-18 实测核对）
头条两条数据线的图片地址指向**两个不同、且新旧不一致的图床**，匿名可下载性分裂，属上游数据之差，不是本协议代码问题：

| 接口 | JSON 图片字段 | 图床域名 | 匿名实测 |
|---|---|---|---|
| 热榜 `hot-board` | `Image.url` | `p*-sign.toutiaoimg.com`（新图床，带 x 签名） | 200，可正常下载 |
| 实时 feed 流 | `image_url` | `//p*.pstatp.com/...`（旧字节图床 pstatp，裸地址、无签名） | 000（域名无法建立连接，非 4xx） |

## 实时 feed 图片失效说明（2026-09-18 实测）
- 实时 feed 接口 `/api/pc/feed/` 本身健康（今天实测 200，返回 8 条实时新闻）。
- 但其返回的 `image_url` 全部是头条**已废弃的旧图床 pstatp.com** 的裸地址，该域名当前对公网**连 TCP 都建立不上**（code=000，而非 404/403），且 feed 记录属于今天、并非历史缓存。
- 同一响应里**热榜图片走新图床 toutiaoimg（带签名、可下载 200）**，与 feed 流分裂——这是头条上游数据固有的两条线，客户端如实转发、无需也无法在本地统一。
- 结论：**无需改动代码**；如需调试，只看热榜图片即可（可用），feed 实时流图片为上游数据合理失效。