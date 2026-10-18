# fbprotocols/chargectl — 本机电池充电限额

纯本地后端：fedbridge 运行期间,对**本机电池**施加一个充电窗口,
让电池长期停留在中间电量,而不是一直顶在 100%。**不 publish、不接收、无
凭据、无状态文件、无命令行 flag、无 build tag**(与 `sysinfo` 一样总是编译)。
窗口默认 `>=75%` 停充、`<=30%` 恢复,经运行时设置可改;每 `30s` 复查一次。

| 项 | 值 |
| --- | --- |
| build tag | 无(总是编译) |
| 注册文件 | `fedbridge/chargectl.go` |
| 命令行 flag | 无(改用运行时设置 `chargectl-pause-at` / `chargectl-resume-at`) |
| 状态文件 | 无(不持久化) |
| AuthStatus | `local`(无凭据);macOS 只读为 `read-only`、Linux 无可写节点为 `no-backend` |
| Capacities | 都不置位(既不发送也不接收) |
| 停充阈值 | 默认 `DefaultPauseAt = 75`(运行时设置 `chargectl-pause-at`) |
| 恢复阈值 | 默认 `DefaultResumeAt = 30`(运行时设置 `chargectl-resume-at`) |
| 复查间隔 | `Interval = 30s` |

```
cd fedbridge && go build -v -tags gomuks,toxoverhttp,outlookgraph,emailimap,zhihu,xhs,toutiao,weibo,coolapk,hongguo,bdtieba,bilibili,irccloud,irclounge,matrixlite
# chargectl 无需 tag 即包含在二进制中,随进程启动
```

## 运行时设置

两个设置**没有命令行 flag**,只经以下三处,且均为 **hot**(改后下一 tick 生效,
无需重启):

| 设置项 | 环境变量 | 默认 | 范围/约束 |
| --- | --- | --- | --- |
| `chargectl-pause-at` | `FEDLET_CHARGECTL_PAUSE_AT` | `75` | 整数 `1..100` |
| `chargectl-resume-at` | `FEDLET_CHARGECTL_RESUME_AT` | `30` | 整数 `0..100`,且 `< pause` |

来源:网页 `Config` 页(写 `~/.config/fedlet/config.json`)、上述环境变量、
`config.json`。**成对整体校验**:网页 PATCH 非法返回 `400`,提示含范围
(如「停充阈值需为 1..100 的整数(当前 150)」「恢复阈值需小于停充阈值」);
环境变量/文件非法则记一条日志并忽略(保留默认/上次值)。

## 控制方式

`probeController()` 按顺序展开候选表,取第一个**存在且可写**的节点。三态:

| 模式 | 节点形态 | 停充 | 恢复 |
| --- | --- | --- | --- |
| `threshold` | 内核 ABI 一对节点 | 写 `charge_control_end_threshold=75`、`..._start_threshold=30` | 同上(恒置窗口,回滞由内核负责) |
| `level` | 内核 ABI 仅 end 节点 | 写 `charge_control_end_threshold=75` | 同节点:低于 75% 即自行充回,无需额外步骤 |
| `switch` | 单个厂商布尔/开关节点 | 写 `disableVal` | 写 `enableVal` |

候选按顺序探测:内核 ABI(`kernelCandidates`)无条件优先,其次首选厂商表,
再次其余内核节点(`charge_behaviour`),最后是固定上限的笔记本兜底
(`laptopExtras`,当前为 IdeaPad `conservation_mode`)。厂商表先后由包内
`var preferACC = true` 决定:

- `nodes_acc.go` — 译自 ACC 的 `ctrl-files.sh` 开关表(默认优先);
- `nodes_abi.go` — 自研内核 ABI 表(后备)。

`charge_stop_level`(Google/LGE)属 end-level 语义,与
`charge_control_end_threshold` 一样恒写 `75`,不再使用"写当前容量"的 `@capacity`
伪值(满电时写 `100` 等于不停充)。`writeValue` 是**懒写**:先读回,值相同就不
写;失败重试 6 次、间隔 1s,每次重试先写 `值+\n`,被拒再写裸值。

阈值/end-only 模式在写阈值前会 best-effort 写 `charge_type=Custom`——Dell 等
驱动只有在 `Custom` 下才让 `charge_control_*` 生效(KDE #497200);写失败时保持
未完成状态,下一轮重试(失败日志只打一次)。

每轮决策(`checkOnce`):

1. 枚举 `power_supply`,找 `type == "Battery"` 且未 `present=0` 的电池(无电池 →
   记一次错误并空转);
2. **多信号插电判定** `readPlugged()`:任一外接电源 `online>0`、或 `present>0`、
   或电池 `status` 为 `Charging`/`Full`、或 `current_now>40mA`,即视为插电,插拔各
   记一条日志。外接设备名兼容 ACC 的 `ac`/`usb`/`dc`/`mains`/`pc_port` 及
   `mtk-*`、`smb*` 前缀。各厂商上报五花八门(曾因此恒判为"未插线"导致停充被跳过、
   一路充到 100%),故宁可多判 true;
3. `threshold` 模式:恒置 `start=30/end=75` 后返回;
4. `level` 模式:恒置 `end=75` 后返回;
5. `switch` 模式:**不再以"已插线"为前提**,始终按容量**无状态**决定——`capacity>=75`
   写停、`capacity<=30` 写启、中间区间不动(回滞);`lastState` 仅用于日志去重。停/启
   写入是幂等的,放电时多写一次无害,而漏判插电才是回到 100% 的根因。因不记忆状态,
   重启后能自动纠正上次遗留的停充/恢复态;
6. 有效性告警:阈值/level/switch 模式下,若**插电**且 `capacity>=75`,而容量仍在
   **上升**(相邻 tick 对比,主信号,能识破"写得进去但不生效"的节点,即使内核谎报
   `status`/`current`)或电池仍 `status=Charging`/`Full` 且 `|current_now|>40mA`
   (辅信号),记一条 warning,提示当前节点可能无效;未插电或容量回落时清零。
7. **自动后端回退**:有效性告警连续累计到 `effectiveStrikes=3` 次,或连续写失败累计到
   `reprobeAfter=3` 次时,把当前节点加入跳过集并 `probeControllerSkip()`,自动切到下一个
   可写节点;都不可用时保留原节点继续重试。写成功后计数清零。
8. **不再放弃**:启动时找不到任何可写节点也不再 `setRunning(false)` 退出,而是标记
   `AuthStatus=no-backend` 并每 tick 重探——节点可能因模块加载、SELinux relabel、
   驱动重绑而稍后出现。

## 权限探测

`probeAccess()` 用 `unix.Access(path, W_OK)` 判定可写性,失败按 errno 归类打日志
(`EACCES`/`EPERM`/`EROFS`/`ENOENT`/`ENOTDIR`),便于从日志直接定位 Android 上
SELinux 拒写的问题:很多机型节点能读、写被拒。进程以 root 运行时 `Access` 恒通过。

## 内核 ABI

- `charge_control_start_threshold` / `charge_control_end_threshold`:
  Linux **4.20(2018-12 合并,2019-03 发布)** 加入的 power_supply 标准节点。
- `charge_behaviour`(`auto` / `inhibit-charge` / `force-discharge`):更晚加入。
- `charge_stop_level`(Google/LGE 等):单一百分比"停止档",按 end-level 语义使用。
- Android 6~10 基本无上述标准节点;Android 12+ 部分机型才有,故主要靠厂商开关回退。
- 未插线时的探测"按可写接受",不写入,标记为未实测。

参考:
- 内核文档 `Documentation/ABI/testing/sysfs-class-power`
- ACC: <https://github.com/VR-25/acc>

## 已知硬件怪癖

- `battery/store_mode`(Samsung):触发时把电量冻在当前值,拔线数秒才解冻;ACC 默认
  禁用它,故本表将其排在 `battery/batt_slate_mode` 之后。
- `VPC2004:*/conservation_mode`(IdeaPad):布尔开关,`1` 把上限压到约 50-60%、
  `0` 恢复充满;是**固定**上限,无法设成 75%,故排在所有百分比节点之后仅作兜底。
- `charge_stop_level`(Google/LGE):按 end-level 恒写 75;不再用 `@capacity` 写当前
  容量(满电时等于不停充)。
- `charge_control_end_threshold` 的驱动会**四舍五入**写入值,读回为实际值;懒写可能
  每轮重写一次,属正常。
- 单 end 阈值机型(ASUS/Apple)无回滞,会在 75% 附近微循环,这是硬件行为。

## 平台矩阵

| 平台 | 行为 |
| --- | --- |
| Linux 笔记本 | 优先内核 `charge_control_*` 阈值 |
| Android | 优先 ACC 厂商开关;sysfs 被 SELinux 拦时只能记录权限错误 |
| macOS | `ctrl_darwin.go`:读 `ioreg`/`pmset`;写优先原生 SMC(purego),回退外置 `bclm`/`smc`(见「macOS(darwin)」) |
| Windows/FreeBSD | `ctrl_stub.go`:`Start`/`Stop` 空实现,`IsRunning()` 恒 false |

交叉编译:`ctrl_stub.go` 用 `//go:build !linux && !android && !darwin` 兜底,保证非
Linux/macOS 平台 `go vet` 通过。

## 测试

```
go test -race ./fbprotocols/chargectl/
```

| 用例 | 覆盖 |
| --- | --- |
| `TestProbePrefersThreshold` | ABI 表优先时选中阈值节点并写入 75/30 |
| `TestProbeFallsBackToSwitch` | 仅有厂商开关时回退 `battery/store_mode` |
| `TestChargeStopLevelLevel` | `charge_stop_level` 选 `modeLevel` 并写 75 |
| `TestConservationMode` | IdeaPad `conservation_mode` 兜底开关的停/启 |
| `TestChargingNowMissingCurrent` | 无 `current_now` 时不误报在充电 |
| `TestChargingNowFull` | `status=Full` 且有电流时算在充电 |
| `TestPluggedMtkSmb` | `mtk-*`/`smb*` 前缀外接设备识别 |
| `TestEffectivenessTrend` | 高电量仍上升时累计有效性 strike(驱动回退) |
| `TestChargeTypeRetriesAfterFailure` | `charge_type` 写失败保持未完成、后续重试 |
| `TestWriteFailTracking` | 写失败累计/成功清零(重探测依据) |
| `TestHysteresis` | 50→76→76→20 的停/保持/恢复(校验 `lastState`) |
| `TestPlugUnplug` | 插→拔→低位重插的状态机 |
| `TestSwitchStopsWhenNotPlugged` | 未插线但高电量时仍写停(停充不依赖插电判定) |
| `TestExternalChargerTypes` | 各类 USB_* / Wireless / WPC 的 `online` 识别 |
| `TestPluggedFromBattery` | 无外接设备时用电池 `status`/`current_now` 兜底 |
| `TestPluggedPresentOnly` | 仅 `present=1` 也判为插电 |
| `TestLevelMode` | 仅有 end 阈值时选 `modeLevel` 并写 75 |
| `TestChargeTypeCustom` | 阈值写入前 best-effort 写 `charge_types=Custom` |
| `TestThresholdDegrade` | start 写失败时降级为 end-only |
| `TestStatelessSwitch` | 遗留停充态下中间区间不动、低电量恢复 |
| `TestWriteAddsNewline` | 写入带尾随换行 |
| `TestNoBatteryIdle` | 无电池时记错误、不 panic |
| `TestProbeAccess` | 只读节点判定 + errno 归类 |
| `TestParseIoregBattery` | `ioreg` 解析(Intel mAh 换算百分比) |
| `TestParseIoregBatteryAS` | Apple silicon `MaxCapacity=100` 时直接用百分比 |
| `TestParsePmset` | `pmset -g batt` 回退解析 |
| `TestParsePmsetDischarging` | `discharging` 不被误判为充电 |
| `TestBatteryOnAC` | `ExternalConnected`/`IsCharging`/`FullyCharged` 任一为真即插电 |
| `TestFourCC` / `TestKeyName` | SMC 键的 FourCC 打包/还原 |
| `TestU32RoundTrip` / `TestHexBytes` | SMC 大端整数与 `smc -w` 十六进制编码 |
| `TestLE32RoundTrip` | 固件限额键的小端 ui32 编解码 |
| `TestSmcStopAllow` | CH0B/CHTE/CHWA/CH0I/CH0J/CH0K/CHIE 的停/启取值 |
| `TestSMCLayout` | 80 字节 `SMCKeyData_t` 关键偏移(含 `result`)防回归 |
| `TestSMCResultName` | SMC `result` 字节(0/0x84/其他)的可读名称 |
| `TestBFCLValue` | Intel `BFCL=value-5` 的边界收敛 |
| `TestFindTool` | 外置工具显式目录探测(可执行性判定) |

测试用 `sandbox()` 把 `sysRoot` 指向临时目录,并用 fixture 目录模拟
`/sys/class/power_supply` 与厂商节点(`ctrl_linux.go`,仅 linux/android)。
`mac_read.go` 与 `smc_codec.go` 的用例无 build tag,在任意平台可跑。

## macOS(darwin)

无 `/sys/power_supply` 树,SMC 充电原子与 BCLM 只能经 IOKit 访问。本后端
**不用 cgo**(需 macOS SDK、破坏从 Linux/CI 交叉编译),而是用
[purego](https://github.com/ebitengine/purego) 在运行时 `dlopen` IOKit,直接调用
`AppleSMC` 的 `IOConnectCallStructMethod`(80 字节 `SMCKeyData_t`,selector=2)。
仅在原生不可用/无效时回退 shell 调外置 `bclm`/`smc`。**原生路径未在真机实测**。

| 后端 | 读 | 写 | 备注 |
| --- | --- | --- | --- |
| 原生 SMC 固件限额 `bfF0` | `ioreg -rn AppleSmartBattery`(回退 `pmset -g batt`) | `bfF0=00`→`bfD0=upper`→`bfE0=lower`→`bfF0=02` | macOS 27 代起,固件自执行窗口(小端 ui32);三键齐备才启用 |
| 原生 SMC BCLM (Intel) | 同上 | `BCLM=<pauseAt>` | level 语义,充到 pause 停 |
| 原生 SMC CH0B/CH0C | 同上 | `CH0B=02`/`00`,并同步 `CH0C` | Apple silicon |
| 原生 SMC CHTE | 同上 | `CHTE=01000000`/`00000000` | Tahoe 起 |
| 原生 SMC 适配器 `CHIE`/`CH0J`/`CH0I`/`CH0K` | 同上 | 切断墙电(`CHIE=08`、其余 `01`)/恢复(`00`) | **适配器模式**:上限靠放电维持,插电时低于 `pause-5` 才恢复,避免频繁通断 |
| 原生 SMC CHWA | 同上 | `CHWA=01`(启用 80% 上限)/`00` | 固定 80%,非任意档 |
| 外置 `bclm` | 同上 | `bclm write <pauseAt>` | 仅当原生缺席 |
| 外置 `smc` | 同上 | `smc -k CH0B -w 02`(停)/`00`(允),并同步 `CH0C` | 仅当原生缺席 |

- 后端探测顺序(逐个能力探测,**不按 macOS 版本分支**):
  原生 `BCLM` → 原生 `CH0B` → 原生 `CHTE` → 原生 `CHWA` → 外置 `bclm` → 外置
  `smc`(试 `CH0B`→`CHTE`→`CHWA`);全部不可用则**只读**(记一次 read-only 告警,
  不反复刷屏)。
- 插电判定:`readPlugged` 由 `ExternalConnected` / `IsCharging` / `FullyCharged`
  **任一为真**即判为插电,避免个别机型 `ExternalConnected` 缺失时误判。
- **自动回退**:某后端连续写失败 `reprobeAfter=3` 次,或连续 `effectiveStrikes=3`
  次"高电量仍在充电"却无效时,自动切到下一个后端;直至只剩只读。
- 提权:原生走 IOKit(需 root/相应 entitlement,fedbridge 以 root 运行为宜);外置
  工具默认**直接执行**,非 root 时设 `FEDLET_CHARGECTL_SUDO="sudo -n"` 注入前缀,
  `none` 表示不提权。
- env 覆盖:`FEDLET_CHARGECTL_SMC`(默认 `smc`)、`FEDLET_CHARGECTL_BCLM`(默认
  `bclm`)、`FEDLET_CHARGECTL_SUDO`。**不再有 `FEDLET_CHARGECTL_MAC_MODE`**——后端
  一律自动探测。
- 稳健性:所有外部工具调用 5s 超时;只读降级经 `AuthStatus` 暴露为 `read-only`、
  无可写节点时为 `no-backend`;`CHWA`(固定 80%)命中时打印忽略 75/30 的告警。
- **日志前缀**:所有日志经标准 `log` 在输出处直接带 `chargectl: ` 前缀(与
  `emailimap:`/`weibo:`/`sysinfo:` 一致),`grep chargectl` 即可捞出本后端全部输出。
- **写入校验(与版本无关)**:每次原生写后**回读比对**,并校验 80 字节
  `SMCKeyData_t.result` 字节(非 0 即视为被 SMC 拒绝,如 `0x84` key not found、
  `0x86` not writable)。被拒或“写了不生效”即触发后端回退,绝不静默成功。
  写 `BCLM` 成功且机器存在 `BFCL` 时,同步写 `BFCL=value-5`(仅 MagSafe LED 指示,
  不决定停充)。
- **系统可能覆盖该键**:写充电限额需 root;macOS 自带的电池健康管理 /
  Optimized Battery Charging 会改写同一 SMC 状态。本后端每 `30s` 重写并回读,
  若发现被覆盖,建议在系统设置中关闭该功能。Intel 机上 macOS 可能多充约 3%
  (设 75 实际约 77–78),属正常。
- **外置工具查找**:除 `$PATH` 外还显式探测 `/usr/local/bin`、`/opt/homebrew/bin`、
  `/opt/local/bin`、`/usr/local/sbin`、`/usr/bin`——以 root/daemon 运行时 `PATH`
  常不含这些目录。
- **启动诊断**:原生 SMC 打开成功时打印一次键清单
  (`bfF0/bfD0/bfE0/BCLM/BFCL/CH0B/CH0C/CHTE/CHIE/CH0J/CH0I/CH0K/CHWA` 的存在与
  数据长度);打开失败则打印 `chargectl: darwin: native SMC unavailable (...)`。
  一条日志即可区分“键缺失”“SMC 打不开”“工具找不到”“写入被拒”四种情况。
- **现代 macOS 限制(root 不能绕过)**:BCLM 在 macOS `>= 15.0` 为 no-op(需关 SIP);
  `CH0B/CH0C/CHTE` 在 `26.7+`/`27 beta` 即使 root 也返回 `kIOReturnNotPrivileged`
  (此时 `CH0B/CH0C/CHTE` 常退化为零长度占位键,`keySize` 直接判缺)。失效即自动回退
  下一后端。若机器暴露 `bfF0/bfD0/bfE0`,本后端按固件限额路径写入;若只剩适配器键
  (`CHIE` 等),则退为适配器模式(切断墙电、以电池放电压住上限)。后端顺序:
  `固件 bfF0 → BCLM → CH0B/CH0C → CHTE → 适配器 CHIE/CH0J/CH0I/CH0K → CHWA`。
  适配器与固件路径**未在真机实测**,取值依据 `batt`
  (<https://github.com/charlie0129/batt>,`pkg/smc/charging.go`/`adapter.go`)。

## 许可注记


`nodes_acc.go` 的节点清单译自 **ACC**(Advanced Charging Controller,
<https://github.com/VR-25/acc>),该项目为 **GPL-3.0**。此处仅重新录入硬件节点名
及其允许/停止取值,并裁掉了电流/电压/温度调优、快充以及上游标记为
experimental/deprecated 的条目。若分发本仓库,请注意该表与 ACC 的许可关系。
