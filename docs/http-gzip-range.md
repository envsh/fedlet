# HTTP Gzip 压缩与 Range 请求共存方案

## 问题

`http.FileServer` 原生支持 HTTP Range 请求（断点续传、视频拖动等），
通过 `Accept-Ranges: bytes` 头和 `Content-Range` 响应头实现文件的部分下载。

gzip 压缩会改变响应体的字节流，导致：
- **字节偏移不一致**：压缩后的 Content-Length 和字节偏移不对应原始文件
- **无法从中间位置解压**：gzip 流需要从头开始解压，分段的 gzip 数据不可独立解压
- **输出不稳定**：on-the-fly 压缩结果取决于压缩等级、代理缓冲等因素，同一资源的每次压缩输出可能不同

因此两者在 HTTP 规范层面互斥。

## 业界调研

### nginx

- **`ngx_http_gzip_module`**（动态 gzip）：
  - 当上游响应包含 `Accept-Ranges: bytes` 头时，**自动禁用 gzip 压缩**
  - 2018 年社区确认此行为：`gzip doesn't work while backend response include a Accept-Ranges header`
  - 原因：gzip filter 输出不稳定，不适合 byte range 请求

- **`ngx_http_gzip_static`**（预压缩文件）：
  - 2023 年（ticket #2349）才加入了 Range 支持
  - 前提：文件已预压缩，ETag 和 Content-Length 在请求间一致
  - 预压缩文件的二进制表示是稳定的，相同 ETag 可正确匹配 Range

- **关键引用**（nginx 开发者 Igor Sysoev）：
  > "With gzipping on the fly via gzip filter result is not something stable —
  > it may change depending on various settings, as well as various timing factors."

### Apache

- **`mod_deflate`**：自动对 Range 请求禁用压缩
- Apache 2.3.8+ 测试确认：带 `Range` 头的请求返回 `Content-Encoding: gzip` + `Content-Range`，
  但 curl 尝试解压时返回 `invalid distance code`，说明客户端无法处理 range+gzip 的组合

### Cloudflare

- 文档明确说明："Range requests are incompatible with compression (gzip, brotli)."
- 当 `Range` 头存在时，自动禁用动态压缩

### Go HTTP 框架

- **Fiber (gofiber)**：[compress 中间件](https://docs.gofiber.io/next/middleware/compress/) 明确跳过 `Range` 请求
  - `shouldSkip` 逻辑包括：Range 头存在、206 响应、no-transform 等
  - 2025 年 PR #3745 增强了 RFC 合规性

- **Echo (labstack)**：[gzip 中间件](https://github.com/labstack/echo/blob/master/middleware/compress.go) 通过 `Skipper` 函数支持跳过特定路由，用户可配置 Range 请求跳过

- **Gorilla Handlers**：issue #215 讨论了如何对特定路由（如文件下载）排除 gzip 压缩

### Nginx 社区结论

> "nginx currently does not support range requests in responses that include
> `Content-Encoding: gzip`, the valid argument being that the industry-wide misuse
> of `Content-Encoding: gzip` ... makes it exceedingly hard to be able to satisfy
> such range requests."

## 方案

在 gzip 中间件外层加一层判断：**请求存在 `Range` 头时不压缩，直接透传**。

```
请求 ──→ Range 头存在？ ──yes──→ 直接透传（FileServer 原生支持 Range）
                │
                no
                ↓
          gzip 压缩后响应
```

### 为什么只检查 `Range` 头就够了

- `If-Range` 头必须与 `Range` 头配合使用，单独出现无意义
- `Accept-Encoding: gzip` 是浏览器的默认行为，和 Range 请求同时出现是常态——此时优先服务于 Range
- nginx/Apache/Cloudflare 均采用同样的判断方式

## 实现

### 依赖

使用 `github.com/NYTimes/gziphandler` v1.1.1（最轻量，只依赖 stdlib）。

```bash
cd fedbridge && go get github.com/NYTimes/gziphandler@v1.1.1
```

### 新增文件 `gzipmw.go`

```go
package main

import (
    "net/http"

    "github.com/NYTimes/gziphandler"
)

func gzipUnlessRange(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("Range") == "" {
            gziphandler.GzipHandler(next).ServeHTTP(w, r)
        } else {
            next.ServeHTTP(w, r)
        }
    })
}
```

### 修改 `main.go`

```diff
 import (
     ...
     "net/http"
     ...
+    "github.com/NYTimes/gziphandler"
 )

-    err := http.ListenAndServe(":4004", nil)
+    err := http.ListenAndServe(":4004", gzipUnlessRange(http.DefaultServeMux))
```

所有通过 `DefaultServeMux` 注册的路径（`/stfile/`、`/api/*`、`/p2pin/*` 等）均受此规则约束。

## 注意事项

- `gziphandler` 会自动设置 `Vary: Accept-Encoding` 头，避免 CDN 缓存混乱
- `gzipUnlessRange` 只检查请求的 `Range` 头，不检查响应——因为压缩发生在响应写入之前
- 如果需要更细粒度的控制（如按 Content-Type 白名单过滤），可改用 `GzipHandlerWithOpts`
- 此方案与 nginx、Apache、Cloudflare 等生产级系统的做法一致

## 参考文献

- [nginx gzip module docs](https://nginx.org/en/docs/http/ngx_http_gzip_module.html)
- [nginx gzip + Accept-Ranges conflict (2018)](http://mailman.nginx.org/pipermail/nginx/2018-June/056411.html)
- [nginx gzip_static range support ticket #2349 (2023)](https://trac.nginx.org/nginx/ticket/2349)
- [Fiber compress middleware - skip Range requests](https://docs.gofiber.io/next/middleware/compress/)
- [Echo gzip middleware - Skipper pattern](https://github.com/labstack/echo/blob/master/middleware/compress.go)
- [Cloudflare - Range and compression incompatibility](https://docs.cloudflare.com/speed/optimization/content/brotli/content-rules/)
- [NYTimes/gziphandler](https://github.com/NYTimes/gziphandler)
- [RFC 7233 - HTTP Range Requests](https://datatracker.ietf.org/doc/rfc7233)
