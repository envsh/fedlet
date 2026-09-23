# hongguo — 红果短剧热榜协议

fedlet 的红果短剧接入协议。**单轨、全匿名**(免登录/免 cookie/免签名/免 UA 伪装),与 toutiao 同属
「一个裸 GET + JSON 解析 + 去重发布」的最简拉取节点,但只有**热榜单轨**:红果官网短剧专区
(`hongguoduanju.com/category/real-drama`)是公开、服务端渲染且免会话的图文页,没有 toutiao 那套
实时资讯流,也没有任何登录面。

## 拉取 / 热榜(单轨)

- **`kind:"hotlist"`**:`GET https://hongguoduanju.com/category/real-drama`(短剧专区,
  匿名,单 GET,免认证)。
- 页面内嵌 `_ROUTER_DATA = {…}` JSON(无 `window.` 前缀;或同数据的
  `<script id="__MODERN_ROUTER_DATA__">` JSON 节点,提取自动回退);递归
  `loaderData` 树定位第一个带 `recommendList[]` 的卡片数组,映射为**热榜条目**:
  - `rank`(1 起)、`series_id`(**19 位大整数,`json.Number` 保精度,防丢尾数**)、
    `title`(`series_name`)、`url`(**`/detail?series_id=` 形式,区别于 toutiao 的
    `/trending/{id}/`,勿混接**)、`cover`、`tags`、`episode_cnt`;
  - 原卡片整份以 `Raw` 透传(`series_id`/`series_name`/`series_cover`/`series_intro`/
    `tags`/`episode_cnt`,任一端字段号改动均不失真、不空发)。链接前缀见 AGENTS.md。
- 去重:**按 `series_id` 仅发布新上榜条目**;默认轮询 **600s**(对齐 toutiao,匿名无会话、无理由更慢);72h 去重窗口(seen 表,
  周期惰性淘汰 + 重启装载)。**首轮发布当前全榜**(空 seen 无全新差,与 bilibili 同款),之后仅发新增。
- 空 `recommendList` 与缺失/截断的 `_ROUTER_DATA` 一律按**硬错误**处理(不当作
  "空榜"快乐路径),只入 `LastErrs()`,不打断对方。

## 状态
- 轮询+去重状态持久化 `~/.config/fedlet/hongguo-state.json`(72h 窗口,与 toutiao 同风格)。
- 重启装载 seen 窗口:**不会因重启而重播种**,页面未变则始终不发重复数据。
- `Status()`:`IsRunning`/`AuthStatus("anonymous")`/`ConnectedSince`/`LastErrs`,与
  其它协议生命周期一致。

## 签名 / 认证
- **无**。`AuthStatus()` 恒 `"anonymous"`,`AuthUser()` 恒 `"hongguo"`(匿名占位)。
- 无需登录、cookie、UA 伪装或 Referer:纯公开版面。

## 待实测 / 风险
- `_ROUTER_DATA` 内嵌键名(如 `recommendList`)与 `loaderData` 层级可能随站点改版
  变动:测试已覆盖「键变体 / 空列表 / 缺失 marker / 截断 JSON」四条分支,线上遇变即红、
  不静默空发。
- `series_id` 为 19 位整数:JSON 数字字面量解析用 `json.Number` 保持精度(测试绑定
  `7111943588446997534` 等 fixture 复核往返)。

## 文件
```
hotlist.go     热榜端点与解析(_ROUTER_DATA 提取 + recommendList 递归 + json.Number 保精度)
hongguo.go     轮询循环 / Start/Stop / 去重状态 / 状态面 / SetPublishInfo 匿名发布
hotlist_test.go 离线 fixture 单测(解析 / rank/url/tags/episode_cnt / 键变体 / 空榜 / 截断)
```

## 构建
```
cd fedbridge && go build -v -tags gomuks,toxoverhttp,outlookgraph,emailimap,zhihu,xhs,toutiao,weibo,coolapk,hongguo
```
