# fbprotocols/sysinfo — 本机系统指标采集

不拉取任何外部数据的后端：定时对本机做一次全量采样，把结果整份序列化为
**一条**消息发布到 P2P（topic 由 fedbridge 的 `channel_name` 决定，当前为
`reddit`）。没有任何凭据、不需要网络访问（除发布走 `127.0.0.1:4004`）。

| 项 | 值 |
| --- | --- |
| build tag | `sysinfo` |
| 注册文件 | `fedbridge/sysinfo.go` |
| flag | `-sysinfo-interval`（默认 `123s`） |
| 状态文件 | `~/.config/fedlet/sysinfo-state.json`（只存 `last_ok_at` + 最近 3 条错误，**不存指标**） |
| proto_type | `sysinfo` |
| cycle_count | `1`（一轮一条，与列表型协议的"本轮条数"同语义） |
| AuthStatus | `local` |

```
cd fedbridge && go build -v -tags sysinfo
./main -sysinfo-interval 123s
```

## 采集内容

每轮生成一个 `Snapshot`，九个小节全部总是出现（取不到就是空数组或 `-1`，
不会省略键）：

| 小节 | 内容 | 数据源 |
| --- | --- | --- |
| `host` | 主机名/OS/平台/内核/架构/虚拟化/进程数/uptime/boot_time | gopsutil `host` |
| `cpu` | 总体与每核 `percent`、逻辑/物理核数、型号、主频 | gopsutil `cpu`（自算差值） |
| `mem` | 内存与 swap 的 total/available/used/free/used_percent | gopsutil `mem` |
| `load` | `load1` / `load5` / `load15` | gopsutil `load` |
| `disk` | 每个已挂载文件系统：设备/挂载点/文件系统/容量/inode | gopsutil `disk`（`Partitions(false)`，已滤 proc/sysfs 等 pseudo fs） |
| `io` | 每块设备的读写字节数/次数 + `read_speed`/`write_speed` | gopsutil `disk.IOCounters` + 上一轮差值 |
| `net` | 每网卡的 mac/flags/字节包计数/错误丢包 + `send_speed`/`recv_speed` | gopsutil `net` + 上一轮差值 |
| `temp` | 每个传感器的 `celsius`/`high`/`critical` | gopsutil `sensors` |
| `battery` | 见下表 | 自实现（见平台矩阵） |

`errors[]` 附在顶层，记录本轮所有失败原因（去重 + 上限 32 条）。

### battery 字段

| 字段 | 单位 | 说明 |
| --- | --- | --- |
| `index` / `present` | – | `present` 为三态 |
| `percent` | % | 相对满充容量 |
| `charging` / `ac_power` | – | 三态 |
| `current` / `full` / `design` | mWh | 三种平台统一到 mWh |
| `health_percent` | % | `full / design × 100` |
| `health` | 文本 | `"Good"` / `"Failed"` / `""` |
| `cycles` | 次 | **不支持时报 -1，不报 0** |
| `state` | 文本 | `Charging` / `Discharging` / `Full` / `Idle` / `""` |
| `charge_rate` | mW | 充电为正、放电为负 |
| `voltage` | V | |
| `temperature` | °C | |

## `-1` 与三态规则

| 规则 | 场景 | 处理 |
| --- | --- | --- |
| **A 函数级** | gopsutil 调用返回 `err` | 该小节数值全 `-1`，`errors[]` 记 `"section: err"` |
| **B 字段级** | 值为 NaN / ±Inf / 负数 / 分母为 0 | 单字段变 `-1`，同小节其余字段照常发布 |
| **C 首轮差值** | 需要上一轮才能算的量（`cpu.percent`、`per_cpu`、`io.*_speed`、`net.*_speed`） | 首轮一律 `-1`；计数器回退（设备重置）也判 `-1` |
| **D 单项失败** | 数组里某一项读取失败（某个挂载点、某块设备、某块电池） | 该项字段 `-1`，数组仍发布，其余项正常 |
| **E 整类缺失** | 整类拿不到数据（台式机无电池、VM 无温度传感器、平台未实现） | **空数组 `[]` + `errors[]` 写明原因**，绝不发 `-1` 占位行 |

- **数值**：唯一哨兵 `-1`。`-1` 是 `InvalidNum`，任何真实指标都不会是负数。
- **bool**：`-1` 未知 / `0` 否 / `1` 是（`present`、`charging`、`ac_power`）。
- **string**：未知用 `""`（不发 `null`）。
- **数组**：始终 `[]` 而非 `null`（快照预分配保证）。
- **特例**：swap 总量为 0 是"没配 swap"这一**有效状态**，`swap_used_percent` 报 `0` 而非 `-1`，且不记错误。

```jsonc
{
  "kind": "sysinfo", "published_at": 1770000000,
  "proto_type": "sysinfo", "cycle_count": 1,   // 顶级平铺
  "host":  { "hostname": "box", "uptime_sec": 123456, ... },
  "cpu":   { "percent": -1, "per_cpu": [-1, -1], ... },   // 首轮
  "temp":  [],                                            // 无传感器 → 规则 E
  "battery": [],
  "errors": ["temp: no temperature sensors reported", "battery: no battery present"]
}
```

## 平台矩阵

| 平台 | 采集实现 | 备注 |
| --- | --- | --- |
| Linux | gopsutil + `/sys/class/power_supply` | 全字段（含 `cycle_count`） |
| Android | 同上；sysfs 被 SELinux 拦时回退 `/system/bin/dumpsys battery` | dumpsys **无** design capacity / cycle count → 那两项 `-1` |
| macOS | `ioreg -n AppleSmartBattery -r -a`（XML plist） | 含 `CycleCount`、`DesignCapacity` |
| Windows | SetupAPI 枚举 + `IOCTL_BATTERY_QUERY_*` | 含 `BATTERY_INFORMATION.CycleCount`；接口复用自 `distatus/battery`（MIT，文件头带版权） |
| 其他（FreeBSD 等） | 无电池实现（`batt_stub.go`） | 电池小节走规则 E，其余小节照常 |

交叉编译已验证：`windows/amd64`、`darwin/{arm64,amd64}`、`android/arm64`、
`freebsd/amd64` 四平台 `go vet` 通过。**注意 `GOARCH=arm`（32 位 Android）
需要 NDK/cgo**，请用 `fedbridge/Makefile` 的 `a32` 目标。

## 测试

```
go test -race ./fbprotocols/sysinfo/
```

| 测试文件 | 覆盖 |
| --- | --- |
| `sysinfo_test.go` | 规则 B/C、三态、错误去重与上限、首轮形状、发布平铺字段、state 读写、Start/Stop 时序（含"首轮必须等满一个 interval"）、错误环形 3 条 |
| `batt_linux_test.go` | sysfs fixture（能量型/电荷型换算、无电压时的 `-1`、空电池仓跳过、AC 源合并）、dumpsys 解析、status 映射 |
| `batt_darwin_test.go` | plist 数组/dict 两种形态解码、ioreg 字段换算（mV→V、0.1K→°C、mA×V→mW）、非法字段回落 `-1` |
| `batt_windows_test.go` | 绝对/相对容量、`0xFFFFFFFF` 与 `rate=0x80000000` 哨兵、`PowerState` 标志位→三态 |

## 依赖与约束

- `github.com/shirou/gopsutil/v4 v4.24.12` — **必须锁这个版本**：v4.25.x+ 把
  `go.mod` 抬到 `go 1.23.0`，会触发 toolchain 升级。
- `howett.net/plist v1.0.0` — 只有 macOS 用（解析 ioreg 输出）。
- `golang.org/x/sys` — Windows 的 SetupAPI/DeviceIoControl。
- **不引入** `github.com/microsoftgraph/msgraph-sdk-go`（全仓硬约束）。
- **不引入** `distatus/battery`：它每个平台都会丢字段（macOS 不读 `CycleCount`、
  Windows 定义了 `CycleCount` 却不读、Linux 不读 `cycle_count/health/temp`），
  故按其思路自实现三平台读取。
- 不引入 `AndroidGoLab/binder`：Android 电池走 `dumpsys` 子进程。

## 时序说明

`fedbridge/main.go` 在 `ListenAndServe` **之前**调用 `StartFn`，所以首轮若立即
执行会连不上 `:4004` 导致发布失败。轮询循环因此用 ticker 的**第一个 tick（即
满一个 interval 之后）** 才开始第一轮采样——既避开启动窗口，也让各轮周期对齐。
