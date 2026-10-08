# PLAN — sysinfo 实施记录

本文件记录 `fbprotocols/sysinfo` 的**决策依据与验证结果**（需求与方案的推演过程，
以及执行时留下的可复核结论）。用法与字段说明见 [README.md](README.md)。

## 1. 需求

给 fedlet 加一个本机系统信息采集后端，采完发布到 P2P。用户逐项拍板的选项：

| 维度 | 决定 |
| --- | --- |
| 位置 | `fbprotocols/sysinfo/` |
| 形态 | 自写采集模块嵌进 fedlet（不引入现成 agent） |
| 指标 | 电池、磁盘、CPU/内存/负载、网络/温度/磁盘IO **全选** |
| 平台 | Linux / macOS / Windows / Android **全选** |
| 依赖 | 加 `gopsutil v4.24.12` + plist 到根 `go.mod`（后续修正：电池不用 `distatus/battery`，改为自实现） |
| 整类取不到 | 空数组 + `errors[]` 记原因（**非** `-1` 哨兵项） |
| bool | `-1/0/1` 三态 |
| string | 空串 `""` |
| 数值无效 | `-1` |
| topic | `reddit`（全局 `channel_name`） |
| 周期 | **123s**（用户两次强调），flag `-sysinfo-interval` |
| build tag | `sysinfo` |
| state | 只存状态/错误（`last_ok_at` + 最近 3 条错误），**不存指标** |
| 分期 | 不分期，一版做全 |
| 文档 | 需要记录 |

## 2. 关键调研结论（可复核）

### 2.1 为什么用 gopsutil 而不是 `distatus/battery`

`distatus/battery` 每个平台都会丢字段：

| 平台 | 丢失 |
| --- | --- |
| macOS | 不读 `CycleCount` |
| Windows | `batteryInformation` struct **定义了 `CycleCount` 却从不赋值**（`battery_windows.go` 中 `bi` 只取 `FullChargedCapacity`/`DesignedCapacity`） |
| Linux | 不读 `cycle_count` / `health` / `temperature` |

故：**不引入该依赖**，按其 SetupAPI + IOCTL 思路自实现 Windows 读取（文件头保留
其 MIT 版权），Linux 走 sysfs，macOS 走 `ioreg` plist。

### 2.2 gopsutil 版本锁定

| 版本 | `go.mod` | 结论 |
| --- | --- | --- |
| v4.24.12 | `go 1.18` | ✅ 采用（与根 module 的 `go 1.22.1` 兼容） |
| v4.25.x – v4.26.9 | `go 1.23.0` | ❌ 会抬高 `go` 行并触发 toolchain 升级 |

→ **必须 pin `v4.24.12`**，已写入 AGENTS.md 硬约束。

### 2.3 采样实现要点（对照 gopsutil v4.24.12 源码核实）

| 项 | 核实结论 |
| --- | --- |
| `cpu.Percent(0,false)` | 首次调用返回 `"lastTimes was nil"`，本身契合规则 C；但改为**自存 `cpu.Times` 快照算差值**，可测且不阻塞 |
| `cpu.Times(true)` | `/proc/stat` 从 `line[1:]` 起读，**不含 `cpu-total`**；`percpu=false` 才是总行 → 两次调用独立、不重复计数 |
| `cpu.Times(true)` 空数组 | 文件读不到时返回 `[]TimesStat{}, nil`（**err 为 nil**），必须单独判空 |
| busy/total 口径 | `total = user+system+nice+idle+iowait+irq+softirq+steal`，`busy = total-idle-iowait`；`guest` 已含在 `user` 内，不重复加 |
| `disk.Partitions(false)` | 走 `getFileSystems()`，滤掉 `nodev` pseudo fs |
| 温度 | `sensors.SensorsTemperatures()`；darwin 有 `sensors_darwin.go` + `sensors_darwin_arm64.go` 两份，**Apple Silicon 不缺**；Windows 走 WMI（`yusufpapurcu/wmi`，MVS 自动采纳） |
| `load.Avg()` | Windows 有实现（后台采样）；`load.Misc()` 才是 `ErrNotImplementedError`，本模块不用 `Misc` |
| 电池换算 | Linux：`energy_*` µWh→mWh 直除；`charge_*` µAh 需乘 `voltage_now`：`µAh × µV × 1e-9 = mWh`（已用 4Ah/11V = 44Wh 验证） |
| macOS 温度 | `ioreg` 的 `Temperature` 是 0.1 K：`°C = T/10 − 273.15`；结果须落在 (−100, 150) 内否则 `-1` |
| Windows 结构体 | `BATTERY_INFORMATION` 含 `CycleCount`；IOCTL：`2703424`（TAG）/ `2703428`（INFO）/ `2703436`（STATUS）；`SP_DEVICE_INTERFACE_DETAIL_DATA_W.CbSize` = 6（32 位）/ 8（64 位） |

### 2.4 Android

- `GOOS=android` 隐含 `linux` build tag → gopsutil Linux 实现生效。
- `/sys/class/power_supply/` 常被 SELinux 拦 → 降级 `exec /system/bin/dumpsys battery`。
- dumpsys 无 design capacity / cycle count → 那两项 `-1`（规则 B），**不编造 0**。
- 未引入 `AndroidGoLab/binder`（额外依赖，收益不足）。

### 2.5 时序坑（已复核 `fedbridge/main.go`）

`StartFn` 调用点在 `main.go:215`，`http.ListenAndServe` 在 `main.go:243` —— 首轮若
立即执行会连不上 `:4004`（发布走 `POST http://127.0.0.1:4004/p2pin/send`）。
→ 轮询用 ticker，**第一个 tick（满一个 interval）后**才做首轮采样。

## 3. 交付物

| 文件 | 作用 |
| --- | --- |
| `types.go` | wire schema + `InvalidNum`/三态常量 + `DefaultInterval` |
| `collect.go` | 轮次编排、`errCollector`（去重 + 上限 32）、`guardNum`/`guardPct`/`tri`/`fillInvalid`/`rate`、`speedSnapshot`（规则 C 的上一轮计数） |
| `collect_sys.go` | host / cpu / mem / load |
| `collect_disk.go` | 挂载点容量 + inode |
| `collect_io.go` | 块设备吞吐 + 速率 |
| `collect_net.go` | 网卡计数 + 速率 |
| `collect_temp.go` | 温度传感器 |
| `collect_batt.go` | 电池编排 + 注入点 `readBatteriesFn` |
| `batt_linux.go` | sysfs + Android `dumpsys` 回退 |
| `batt_darwin.go` | `ioreg` plist |
| `batt_windows.go` | SetupAPI + Battery IOCTL（带 MIT 版权头） |
| `batt_stub.go` | 其他平台 |
| `sysinfo.go` | 轮询 / state / 状态面 / 发布 |
| `fedbridge/sysinfo.go` | `//go:build sysinfo` 注册 + `-sysinfo-interval` flag + `StopFn` |
| `*_test.go` × 4 | 规则 A–E、三态、四平台 fixture、循环时序 |

### 现有文件改动

| 文件 | 改动 |
| --- | --- |
| `go.mod` / `go.sum` | + `gopsutil v4.24.12`、`howett.net/plist v1.0.0`（MVS 采纳 `x/sys v0.28.0`、`purego v0.8.1`、`wmi v1.2.4` 等）；`go.sum` 16 → 57 行 |
| `fedbridge/Makefile` | `PROTO_TAGS` 加 `sysinfo`（默认构建包含） |
| `fedbridge/fedletweb.go` | `knownProtocols` 加 sysinfo 条目（探测 flag `-sysinfo-interval`） |
| `AGENTS.md` | 目录树、两处 build tags、**修正过时的 `starters` 注册描述**、根 module 依赖与 gopsutil 版本约束 |
| `readme.md` | 已生效协议列表 + sysinfo 专项说明（发布形态、-1/三态、state 内容） |

## 4. 验证矩阵与结果

| 检查 | 命令 | 结果 |
| --- | --- | --- |
| 格式 | `gofmt -l fbprotocols/sysinfo/` | ✅ 无输出 |
| 静态检查 | `go vet ./fbprotocols/sysinfo/` | ✅ 通过 |
| 单测 + race | `go test -race -count=1 ./fbprotocols/sysinfo/` | ✅ 通过 |
| 覆盖率 | `go test -cover` | ✅ 74.0% |
| Linux 交叉 | 同上（native） | ✅ |
| Windows | `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./...` | ✅（含测试文件） |
| macOS | `GOOS=darwin GOARCH=arm64/amd64 CGO_ENABLED=0 go vet ./...` | ✅（含测试文件） |
| Android | `GOOS=android GOARCH=arm64 CGO_ENABLED=0 go vet ./...` | ✅ |
| FreeBSD（stub） | `GOOS=freebsd GOARCH=amd64 CGO_ENABLED=0 go vet ./...` | ✅ |
| 主程序编译 | `cd fedbridge && go build -tags sysinfo` | ✅ 34 MB 产物 |

> `GOARCH=arm`（32 位 Android）要求 cgo/NDK，需用 `Makefile` 的 `a32` 目标，
> 不在上述纯交叉编译范围内。

### 实现过程中修正的问题

1. `howett.net/plist.Unmarshal` 返回 **2** 个值（写成了 3）。
2. charge→mWh 换算的测试期望写错（把 µAh 当 mAh），fixture 改为 4 Ah/11 V。
3. `interface.Flags` 是 `[]string` 而非 `string`（schema 已随之调整）。
4. `Stop()` 原本不等待 goroutine 退出 → `-race` 报数据竞争（测试 cleanup 恢复
   `pubfn_` 时旧 loop 仍在 `round()` 里读）。改为 `close(stop) → <-done` 同步等待，
   顺带保证重启时不会有两个 loop 并发发布。

## 5. 未做 / 后续可选

- CI workflow 未改：`ci.yml` 的 `go vet ./...` 已覆盖本包；其 build tags 属既定裁剪策略。
- `GOARCH=arm` Android 未在本次验证（需 NDK 环境）。
- 未提供 `StopFn` 之外的运行时重配置（改周期需重启）。
- 电池健康度目前只算 `full/design` 比值；Linux 有 `health` 字符串时直接透传，
  两套语义尚未统一。
