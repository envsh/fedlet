# fbprotocols/chargectl — 本机电池充电限额

纯本地后端：fedbridge 运行期间,对**本机电池**施加一个固定的充电区间,
让电池长期停留在中间电量,而不是一直顶在 100%。**不 publish、不接收、无
凭据、无状态文件、无 flag、无 build tag**(与 `sysinfo` 一样总是编译)。
策略硬编码:`>=75%` 停充,`<=30%` 恢复;每 `30s` 复查一次。

| 项 | 值 |
| --- | --- |
| build tag | 无(总是编译) |
| 注册文件 | `fedbridge/chargectl.go` |
| flag | 无 |
| 状态文件 | 无(不持久化) |
| AuthStatus | `local` |
| Capacities | 都不置位(既不发送也不接收) |
| 停充阈值 | `PauseAt = 75` |
| 恢复阈值 | `ResumeAt = 30` |
| 复查间隔 | `Interval = 30s` |

```
cd fedbridge && go build -v -tags gomuks,toxoverhttp,outlookgraph,emailimap,zhihu,xhs,toutiao,weibo,coolapk,hongguo,bdtieba,bilibili,irccloud,irclounge,matrixlite
# chargectl 无需 tag 即包含在二进制中,随进程启动
```

## 控制方式

`probeController()` 按顺序展开候选表,取第一个**存在且可写**的节点。三态:

| 模式 | 节点形态 | 停充 | 恢复 |
| --- | --- | --- | --- |
| `threshold` | 内核 ABI 一对节点 | 写 `charge_control_end_threshold=75`、`..._start_threshold=30` | 同上(恒置窗口,回滞由内核负责) |
| `switch` | 单个厂商节点 | 写 `disableVal`(可能是 `@capacity` → 写当前容量) | 写 `enableVal` |

候选表两套并存,由包内 `var preferACC = true` 决定先后:

- `nodes_acc.go` — 译自 ACC 的 `ctrl-files.sh` 开关表(默认优先);
- `nodes_abi.go` — 自研内核 ABI 表(后备),含 `charge_control_*`、
  `charge_stop_level`、`charge_behaviour`。

`@capacity` 是伪值:停充时替换为当时读到的 `capacity`,对应 Google/LGE 的
`charge_stop_level` 语义。`switch` 模式的 `writeValue` 是**懒写**:先读回,
值相同就不写;失败重试 6 次、间隔 1s。

每轮决策(`checkOnce`):

1. 枚举 `power_supply`,找 `type == "Battery"` 且未 `present=0` 的电池(无电池 →
   记一次错误并空转);
2. 由 `Mains`/`USB`/`USB_*`/`Wireless` 的 `online` 判断是否插着充电线,插拔各记一条日志;
3. `threshold` 模式:恒置 `start=30/end=75` 后返回;
4. `switch` 模式且已插线:`capacity>=75` 且未停 → 停充;`capacity<=30` 且已停 → 恢复;
   中间区间不动(回滞)。未插线时不动作;重新插线后,若电量仍 `>=75` 则保持停充,
   若已 `<=30` 则恢复。

## 权限探测

`probeAccess()` 用 `unix.Access(path, W_OK)` 判定可写性,失败按 errno 归类打日志
(`EACCES`/`EPERM`/`EROFS`/`ENOENT`/`ENOTDIR`),便于从日志直接定位 Android 上
SELinux 拒写的问题:很多机型节点能读、写被拒。进程以 root 运行时 `Access` 恒通过。

## 内核 ABI

- `charge_control_start_threshold` / `charge_control_end_threshold`:
  Linux **4.20(2018-12 合并,2019-03 发布)** 加入的 power_supply 标准节点。
- `charge_behaviour`(`auto` / `inhibit-charge` / `force-discharge`):更晚加入。
- Android 6~10 基本无上述标准节点;Android 12+ 部分机型才有,故主要靠厂商开关回退。
- 未插线时的探测"按可写接受",不写入,标记为未实测。

参考:
- 内核文档 `Documentation/ABI/testing/sysfs-class-power`
- ACC: <https://github.com/VR-25/acc>

## 平台矩阵

| 平台 | 行为 |
| --- | --- |
| Linux 笔记本 | 优先内核 `charge_control_*` 阈值 |
| Android | 优先 ACC 厂商开关;sysfs 被 SELinux 拦时只能记录权限错误 |
| 其他(macOS/Windows/FreeBSD) | `ctrl_stub.go`:`Start`/`Stop` 空实现,`IsRunning()` 恒 false |

交叉编译:`ctrl_stub.go` 用 `//go:build !linux && !android` 兜底,保证非 Linux 平台
`go vet` 通过。

## 测试

```
go test -race ./fbprotocols/chargectl/
```

| 用例 | 覆盖 |
| --- | --- |
| `TestProbePrefersThreshold` | ABI 表优先时选中阈值节点并写入 75/30 |
| `TestProbeFallsBackToSwitch` | 仅有厂商开关时回退 `battery/store_mode` |
| `TestWriteAtCapacity` | `@capacity` 替换为当前容量,恢复写 100 |
| `TestHysteresis` | 50→76→76→20 的停/保持/恢复 |
| `TestPlugUnplug` | 插→拔→低位重插的状态机 |
| `TestNoBatteryIdle` | 无电池时记错误、不 panic |
| `TestProbeAccess` | 只读节点判定 + errno 归类 |

测试用 `sandbox()` 把 `sysRoot` 指向临时目录,并用 fixture 目录模拟
`/sys/class/power_supply` 与厂商节点。

## 许可注记

`nodes_acc.go` 的节点清单译自 **ACC**(Advanced Charging Controller,
<https://github.com/VR-25/acc>),该项目为 **GPL-3.0**。此处仅重新录入硬件节点名
及其允许/停止取值,并裁掉了电流/电压/温度调优、快充以及上游标记为
experimental/deprecated 的条目。若分发本仓库,请注意该表与 ACC 的许可关系。
