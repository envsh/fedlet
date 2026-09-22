# PLAN: fbprotocols/coolapk 酷安匿名后端

## 1. 由来与目标

酷安(Coolapk)官方 v6 匿名 API(带 X-App-Token 弱签名)提供热榜与更新流,
无需登录/cookie。本后端只做两个拉取点,条目**原样发布**、仅按 `id` 排重。

2026-09 经 RSSHub(lib/routes/coolapk)、DailyHotApi、实测探针三方交叉核对
(probe 均已 200 返回真实数据)。

## 2. 硬约束(用户指定)

- 不自定义条目结构体;条目以服务器原文 JSON 发布(仅顶层追加 house flat 字段)
- 只解析 `id` 做排重(热榜/更新流两张 map)
- 匿名、无登录、无 cookie;不得引入登录态

## 3. 拉取点(仅两种线上)

| 用途 | 间隔 | fragment(#/feed/…) |
|---|---|---|
| hotlist | 600s | `statList?cacheExpires=300&statType=day&sortField=detailnum&title=今日热门&subTitle=&page=1` |
| news | 120s | `digestList?type=0,5,9,8,12,10,11,13&title=最新动态&page=1` |

URL 构造:`https://api.coolapk.com/v6/page/dataList?url=` + `url.QueryEscape(fragment)`
(QueryEscape 产出与线上探针一致的 `%23%2Ffeed%2F…` 编码,测试里有金样钉死)。

备选(不实现,留档):statList 的 likenum/favnum/replynum 三榜(day|7days)、
coolPictureList、headlineV8List、newestList;hotList 实测 data 仅 1 条聚合,弃用;
首页头条 /v6/main/indexV8、看看号/用户动态需持 ID/登录态,超出匿名范围。

## 4. 签名(X-App-Token 弱签,无外部依赖)

    device = 随机小写 UUID(每次请求新生成)
    now    = unix 秒
    pre    = "token://com.coolapk.market/c67ef5943784d09750dcfbb31020f0ab?"
             + md5(str(now)) + "$" + device + "&com.coolapk.market"
    token  = md5(base64(pre)) + device + "0x" + hex(now)

请求头:
    User-Agent: Dalvik/2.1.0 (Linux; U; Android 10; Redmi K30 5G MIUI/V12.0.3.0.QGICMXM)
                (#Build; Redmi; Redmi K30 5G; QKQ1.191222.002 test-keys; 10)
                +CoolMarket/11.0-2101202
    X-Requested-With: XMLHttpRequest
    X-App-Id: com.coolapk.market
    X-App-Token: <token>
    X-Sdk-Int: 29   X-Sdk-Locale: zh-CN
    X-App-Version: 11.0   X-Api-Version: 11   X-App-Code: 2101202

金样:signToken("11111111-1111-4000-8000-111111111111", 1700000000)
  = 1cb5d082b9c20d16fbc599a88a3bedee + 11111111-1111-4000-8000-111111111111 + 0x6553f100

## 5. 响应处理(无条目结构体)

- 信封:`var env map[string]json.RawMessage` 解整包,取 `env["data"]`;
  data 为 "null"/缺失 → 空;再 `json.Unmarshal(raw, &data)` 进 `[]json.RawMessage`,
  **条目原始字节完整保留**
- 逐条窥探:`peekItem(raw)` 用 `json.Decoder + UseNumber` 解到 `map[string]any`,
  仅读 `entityType/id/entities`
  - `entityType=="feed"` → 原样 raw 当作发布条目,id=条目 id
  - `entityType=="card"` → 取 `entities[]` 逐条再组 raw 发布,**card 容器本身不发布**
  - 其它(entityType apk/topic/…)→ 跳过,不发布
- 条目 raw 经 `fbshared.InsertFlatFields(raw, {"proto_type":kind,"cycle_count":n})`
  追加顶层 flat 后 publish(不覆盖已有键)

## 6. 编排(coolapk.go,骨架镜像 toutiao.go)

- `Start(hot, news bool, hotInterval, newsInterval)` 默认 600s/120s;轮询 tick 上限 60s
- 状态(~/.config/fedlet/coolapk-state.json,0600):
  `{"hotlist":{id:seen},"news":{id:seen}}`,72h 剪除,冷启动/缺损回落零集
- 状态面:IsRunning / ConnectedSince / LastErrs[3] / AuthStatus="public" / AuthUser="coolapk"
- `fetchDataListFn` 变量可替换,供离线测试与实弹对照
- 错误:logPrefix("coolapk: …") + pushError;单轮失败不影响另一轮

## 7. 文件清单

    fbprotocols/coolapk/token.go        签名+头+build(随机UUID)
    fbprotocols/coolapk/coolapk.go     编排/拉取/展开/去重/发布/状态
    fbprotocols/coolapk/coolapk_test.go 离线单测(下表)
    fbprotocols/coolapk/README.md       用法/端点/签名说明
    fbprotocols/coolapk/PLAN.md         本方案
    fedbridge/coolapk.go                //go:build coolapk + RegisterProtocol
    AGENTS.md                           构建 tag 列表追加 coolapk

## 8. 测试(全离线)

- TestSignTokenGolden:固定 device/now 匹配金样
- TestFragmentEscapingAlignsWithServer:QueryEscape(newsFragment) 匹配线上实测编码
- TestWalkDataFeedAndCardAndSkip:feed 原样 / card 展开 / 非 feed 跳过
- TestPublishEntriesDedupeAndFlat:第一轮发 2、第二轮 0;载荷含 proto_type/cycle_count 与原字段
- TestStateSaveLoadPrune:缺档回落、往返、72h 剪除

## 9. 注册与验证

- fedbridge/coolapk.go 镜像 toutiao.go:RegisterProtocol(Name"coolapk",
  Ctypes{"coolapk"}, CanReceive, StartFn→SetPublishInfo+publish("coolapk",channel_name,v),
  statusFn)
- 验证顺序:
  1) cd fedbridge && go build -v -tags gomuks,toxoverhttp,outlookgraph,emailimap,zhihu,xhs,toutiao,weibo,coolapk
  2) go vet ./fbprotocols/coolapk/
  3) go test ./fbprotocols/coolapk/
  4) 复跑旧 tag(不含 coolapk)构建,确保无回归
- 实弹(可选,人工):替换 fetchDataListFn 或直接 curl 探针已验 200

## 10. 风险与备注

- 匿名弱签为逆向接口,端点/签名可能随客户端版本漂移(xhs/weibo 匿名路径同级风险)
- 需能直连 api.coolapk.com
- 纯 id 排重、无水位:长停机后会补发积压(与 toutiao 水位语义不同,需可接受)
- xhs S4(captcha)处于施工中(des/image 依赖已 fetch),完成后另行续作