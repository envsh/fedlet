# 隧道/远程访问服务对比

## 一、三大自托管/平台级方案

### Cloudflare Tunnel (`cloudflared`)

| 维度 | 说明 |
|------|------|
| **本质** | SaaS 隧道 — 内网向 Cloudflare 边缘反向建连 |
| **架构** | 星型：内网 cloudflared → Cloudflare 边缘 → 用户 |
| **TLS** | Cloudflare 边缘终止（能看到明文流量）|
| **多路复用** | ✅ 单 cloudflared 多 hostname |
| **协议** | HTTP/HTTPS（TCP 需 Spectrum 付费版）|
| **CGNAT** | ✅ 穿透 |
| **DDoS** | ✅ Cloudflare 全球边缘防护 |
| **成本** | 免费（个人，ToS 限制视频流）|
| **控制权** | ❌ DNS + 证书 + 策略全在 Cloudflare 平台 |
| **最佳** | HTTPS 服务 5 分钟开公网，有 DDoS 防护需求，不介意流量过第三方 |

### NetBird

| 维度 | 说明 |
|------|------|
| **本质** | P2P Mesh VPN（WireGuard）+ 可选反向代理（v0.65+）|
| **架构** | 网状：设备间直连 WireGuard（低延迟），NAT 打不通走 relay |
| **TLS** | 自托管集群或 NetBird Cloud |
| **多路复用** | ✅ Mesh 原生，Reverse Proxy 也支持多域名 |
| **协议** | 任意 TCP/UDP（Mesh），HTTP/HTTPS（Reverse Proxy）|
| **CGNAT** | ✅（relay fallback）|
| **DDoS** | ❌（取决于自己的 infra）|
| **成本** | 全自托管免费（BSD-3/AGPL），Cloud 版付费 |
| **控制权** | ✅ 全自托管，数据不过第三方 |
| **最佳** | 需要设备间直连低延迟 + 同时暴露 web 服务给无客户端用户 |

#### NetBird P2P 协议

NetBird 的 P2P mesh 基于 **WireGuard**：
- 每个设备分配 `100.x.x.x/16` 的 overlay IP
- 控制面（Management Server）协调公钥、NAT 打洞、端点发现
- 直连打不通时 fallback 到 relay 服务器
- 控制面通过 gRPC 通信，数据面纯 WireGuard（UDP 51820）

### Pangolin

| 维度 | 说明 |
|------|------|
| **本质** | 自托管 Hub-and-Spoke 反向代理 + ZTNA（WireGuard）|
| **架构** | 星型：Newt（内网）→ VPS 枢纽（Gerbil + Traefik + Pangolin）→ 用户 |
| **TLS** | 你的 VPS 终止（Let's Encrypt 自动）|
| **多路复用** | ✅ 单个 Site 多 Resource |
| **协议** | HTTP/HTTPS + 浏览器内 SSH/RDP/VNC + 私有 TCP/UDP/CIDR |
| **CGNAT** | ✅ Newt 出站建连穿透 |
| **DDoS** | ❌ 取决于 VPS 提供商的基础防护 |
| **成本** | VPS $4-10/月，社区版免费（AGPL）|
| **控制权** | ✅ 全栈自托管 |
| **最佳** | 想自托管替换 Cloudflare Tunnel，需要浏览器内直接 SSH/RDP/VNC，不接受流量过第三方 |

### 三者核心差异矩阵

| | Cloudflare Tunnel | NetBird | Pangolin |
|---|---|---|---|
| 流量路径 | 用户 → CF 边缘 → 你 | 用户 ↔ peers 直连 / relay | 用户 → VPS 枢纽 → Site |
| TLS 终止点 | Cloudflare 边缘 | 你的 proxy 集群 | 你的 VPS |
| 建连方式 | cloudflared 出站 | WireGuard P2P mesh | Newt 出站 WireGuard |
| 设备直连 | ❌ | ✅ | ❌（都经 VPS）|
| 浏览器内 SSH/RDP | ❌（需 Cloudflare Access + WARP） | ❌ | ✅（v1.19+）|
| 多路复用 | ✅（多 hostname） | ✅（mesh + proxy） | ✅（多资源 per Site）|
| 自托管成本 | $0（控制权在 CF） | $0（全栈自托管） | $4-10/月 VPS |
| 协议范围 | 主要 HTTPS | 任意（mesh）+ HTTPS（proxy） | HTTPS + 浏览器内远程桌面 + 私有 TCP/UDP |
| 社区成熟度 | 生产级 GA | 活跃，Reverse Proxy Beta | 活跃，22K⭐ |

---

## 二、类似 Cloudflare Tunnel 的公开 SaaS 服务

无需自托管 VPS，装客户端或一行 ssh 即可使用。

| 服务 | 连接方式 | 协议 | 免费额度 | 注册 |
|------|---------|------|----------|------|
| **ngrok** | 专用客户端 | HTTP/TCP | 有限带宽，随机域名 | 需要 |
| **LocalXpose** | 专用客户端 | HTTP/TCP/UDP | 有免费计划 | 需要 |
| **Pinggy** | `ssh -p 443 -R0:localhost:3000 pinggy.io` | HTTP/TCP/UDP | ✅ 免费 | 不需要 |
| **localhost.run** | `ssh -R 80:localhost:8080 nokey@localhost.run` | HTTP | ✅ 免费随机域名 | 不需要 |
| **xpos.dev** | `ssh -R 80:localhost:3000 xpos.dev` | HTTP/TCP | ✅ 免费匿名 | 不需要 |
| **Localtunnel** | `npx localtunnel --port 3000` | HTTP | ✅ 免费 | 不需要 |
| **bore** | `bore local 3000 --to bore.pub` | TCP | ✅ 免费 | 不需要 |
| **Tunnelmole** | `tmole 3000` | HTTP | 有免费 | 可选 |
| **zrok** | 专用客户端 | HTTP/TCP | 有免费 | 需要 |
| **Tailscale Funnel** | `tailscale funnel 3000` | HTTPS | 有限（付费扩）| 需要 |

---

## 三、免费额度详细分析

| 服务 | 免费流量 | 隧道数量 | 超时/持久 | 协议支持 | 域名 | 限速 | 注册 |
|------|---------|---------|-----------|---------|------|------|------|
| **ngrok** | **1GB/月**，20k 请求/月 | 3 endpoint | 无超时 | HTTP/HTTPS/TCP | 1 随机 dev 域名 | 4000 req/min | ✅ |
| **LocalXpose** | **带宽不限量** | 2 HTTP 隧道 | 持续 | HTTP/HTTPS only | 随机子域名 | 无明确限速 | ✅ |
| **Pinggy** | **带宽不限量** | 1 隧道 | **60分钟超时断开** | HTTP/HTTPS/TCP/UDP | 随机子域名 | 无明确限速 | ❌ |
| **localhost.run** | 不限量但**限速 ≈1MB/s** | 1 隧道 | 域名几小时变一次 | HTTP only | 随机，频繁变 | credit 限速模型 | ❌ |
| **xpos.dev** | **1GB/天** | 1 隧道 | **3h（匿名）/ 10h（注册）** | HTTP/HTTPS | 随机子域名 | 50 req/s | ❌/可选 |
| **Localtunnel** | 无 SLA | 1 隧道 | 不稳定 | HTTP/HTTPS only | 随机 | 无保障 | ❌ |
| **bore** | 公共实例靠作者自费 | 1 端口 | 持续（TCP 不断） | TCP only | 随机端口 | 无官方限制 | ❌ |
| **Tunnelmole** | 未明确 | **1 并发连接** | 持续 | HTTP/HTTPS | 随机 | 无明确限速 | 可选 |
| **zrok** | **5GB/天（滚动24h）** | 50 share | 持续 | HTTP/HTTPS/TCP | 随机 | **6.66 req/s per IP** | ✅ |

### 各方案定位

- **一直在线稳定用** → **zrok**（5GB/天较宽松）
- **临时调试、带宽大** → **Pinggy**（无限流量但 60min 断）
- **免注册免门槛** → **localhost.run**（HTTP）、**bore**（TCP）
- **功能最全免费** → **LocalXpose**（带宽无限，但仅 HTTP + 2 隧道）
- **成熟首选** → **ngrok**（1GB 月上限，适合轻量调试）
