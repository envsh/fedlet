# macOS sudo + pipe stdout/stderr 丢失问题

## 现象

```bash
# 正常：二进制直接运行 + pipe
./main --help 2>&1 | grep mobile                  # ✅ 有输出

# 正常：sudo 运行，无 pipe
sudo ./main --help                                 # ✅ 有输出

# 异常：sudo + pipe 组合
sudo ./main --help 2>&1 | grep mobile              # ❌ 无输出

# 正常：普通命令 + sudo + pipe（确认 shell pipe 本身没问题）
echo test 2>&1 | grep test                         # ✅ test
sudo echo test 2>&1 | grep test                    # ✅ test
```

## 二进制属性

```
$ ls -la main
-rwsr-xr-x  1 root  wheel  32832144 Jul 14 14:22 main

$ codesign -dvvv main
main: code object is not signed at all
```

| 属性 | 值 |
|------|----|
| 所有者 | root:wheel |
| setuid | `u+s` (`-rwsr-xr-x`) |
| 代码签名 | 未签名 |
| 类型 | Go 1.24.9 darwin/amd64 Mach-O |
| 平台 | macOS (Darwin) / Intel |

---

## 调查结论：setuid root + 未签名 **不是** 根因

### 证据 1：Intel macOS 不限制 Mach-O 二进制的 setuid

Apple SIP（System Integrity Protection）对 setuid 的限制**只针对解释型脚本（`#!`）**，不针对编译好的 Mach-O 二进制。

- Apple Developer Forums: *"Darwin strips setuid from interpreter in hash bang execution"*
- Mach-O 二进制的 setuid bit 在 Intel macOS 上正常工作

**直接证明**: `./main --help 2>&1 | grep` 在无 sudo 的情况下工作，说明 setuid bit 被内核正确遵守，pipe I/O 也正常。

### 证据 2：未签名不影响 pipe I/O

- Intel macOS **不要求**普通二进制代码签名
- 未签名只影响：Gatekeeper、`DYLD_INSERT_LIBRARIES` 注入、notarization
- **不影响** `write()`、`pipe()`、`execve()` 等系统调用

### 证据 3：问题仅在 sudo + pipe 组合出现

```
./main | grep       ✅    sudo ./main           ✅
sudo ./main | grep  ❌    （仅此组合失败）
```

排除二进制属性本身的嫌疑，根因在 sudo 和 pipe 的交互上。

---

## 深度追踪：macOS PTY 数据丢失漏洞

### XNU 内核行为

当 PTY slave 关闭时，XNU 内核的 `ptsclose()` 调用链：

```
ptsclose() → ttyclose() → ttyflush(tp, FREAD | FWRITE)
```

如果 PTY master 尚未读取全部数据，`ttyflush()` 会**丢弃所有未读取的缓冲数据**。这是 POSIX 未定义行为，macOS 的实现导致了数据丢失。

### S_CTTYREF 机制

关键区别在于 `S_CTTYREF` 标记是否设置：

| S_CTTYREF | 子进程 exit() 行为 | 结果 |
|-----------|-------------------|------|
| 未设置 (默认) | slave fd 关闭即释放 PTY | **数据丢失** — master 读到 EOF |
| 已设置 | exit() 调用 `ttywait()`（等效 `TIOCDRAIN`）阻塞等待输出排空 | 数据完整 — 但 `waitpid()` 会阻塞直到 master 读取 |

**XNU 源码中的关键路径** (`bsd/kern/kern_exit.c`):

```
// 如果 S_CTTYREF 已设置，ptsclose() 调用 ttywait()
// ttywait() 等待 pty 输出缓冲排空后才完成关闭
// 如果 S_CTTYREF 未设置，ptsclose() 直接释放，丢弃缓冲
```

### 受影响的项目

此漏洞已被多个项目独立确认并记录：

| 项目 | 链接 | 年份 |
|------|------|------|
| Python Pexpect | [Issue #662](https://github.com/pexpect/pexpect/issues/662) | 2020 |
| Ruby PTY | [Bug #20682](https://bugs.ruby-lang.org/issues/20682) | 2024 |
| Jumpstarter | [PR #837](https://github.com/jumpstarter-dev/jumpstarter/pull/837) | 2024 |
| quick-lint-js | [pty.cpp](https://github.com/quick-lint/quick-lint-js/blob/master/src/quick-lint-js/port/pty.cpp#L205) | 2023 |
| Apple Developer Forums | [Thread #663632](https://developer.apple.com/forums/thread/663632) | 2020 |

### 复现代码（C 语言）

```c
#include <stdlib.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>
#include <util.h>

int main() {
    int tty_fd;
    pid_t pid = forkpty(&tty_fd, NULL, NULL, NULL);
    if (pid == 0) {
        // Child: 写入后立即退出
        write(STDOUT_FILENO, "hello", 5);
        exit(0);
    } else {
        // Parent: 延迟读取 → macOS 上读到 0 字节
        sleep(1);
        char buf[10];
        ssize_t rc = read(tty_fd, buf, sizeof(buf));
        printf("read %zd bytes\n", rc);  // macOS: 0, Linux: 5
        close(tty_fd);
    }
    return 0;
}
```

**在 macOS 上输出**: `read 0 bytes`（数据丢失）
**在 Linux 上输出**: `read 5 bytes: hello`（正常）
**修复方式**: 子进程添加 `close(open("/dev/tty", O_WRONLY))` 以设置 S_CTTYREF

---

## sudo `use_pty` 机制

### sudo 版本信息

```
$ sudo -V
Sudo version 1.9.5p2
Configure options: --with-password-timeout=0 --disable-setreuid
                   --with-env-editor --with-pam --with-libraries=bsm
                   --with-noexec=no --sysconfdir=/private/etc
                   --without-lecture --enable-static-sudoers
                   --with-rundir=/var/db/sudo
```

### use_pty 默认值变更历史

| 版本 | use_pty 默认值 | 说明 |
|------|---------------|------|
| < 1.9.14 | off | 历史行为，直接 execve |
| 1.9.14 | **on** | 改为默认开启（CVE-2005-4890 tty 注入攻击） |
| 1.9.15 | on (bugfix) | 修复 use_pty + 重定向 stdio 到不同终端时输出错乱 |

当前系统为 1.9.5p2，use_pty **默认关闭**，但需检查 `/etc/sudoers` 是否有手动设置。

### sudo_execute() 代码路径

sudo 源码 (`src/exec.c`) 中的执行流程：

```
sudo_execute()
├── CD_BACKGROUND? → fork + exit
├── sudo_needs_pty()?
│   ├── YES → exec_pty()
│   │   ├── 分配 PTY master/slave
│   │   ├── 子进程 stdout → PTY slave
│   │   ├── 父进程读取 PTY master → 写入真实 stdout
│   │   └── ⚠️ 子进程退出 → PTY slave 关闭 → macOS 可能丢数据
│   └── NO  → 进入下一判断
└── CD_SET_TIMEOUT / CD_SUDOEDIT / close != NULL?
    ├── YES → exec_nopty() (fork + 事件循环)
    └── NO  → exec_cmnd() (直接 execve，无 fork)
```

当 `!use_pty` 且无 timeout/edit/close 回调时，sudo 走 `exec_cmnd()` → `execve()` 路径，**不分配 PTY**，子进程直接继承 shell 的 pipe fd，不受 macOS PTY bug 影响。

### use_pty 对 pipe 的影响

```
sudo ./main 2>&1 | grep
├── use_pty OFF → exec_cmnd → execve
│   └── 子进程直接写入 pipe fd → grep 收到数据 ✅
└── use_pty ON  → exec_pty → PTY 分配
    └── 子进程 → PTY slave → PTY master → parent 读取 → pipe → grep
        └── 子进程退出时 macOS ttyflush 可能丢数据 ⚠️
```

### 验证方法

```bash
# 检查当前 sudo 是否使用 PTY
sudo echo -n && [[ "$(tty)" = "$(sudo tty)" ]] && echo 'without PTY' || echo 'with PTY'

# 检查 sudoers 配置
grep -i use_pty /etc/sudoers

# 更完整的检查
sudo tty    # 通常是 /dev/ttysXXX
tty         # 对比是否相同
```

---

## utun 与 setuid root 的依赖关系

### 为什么需要 u+s

二进制通过 `tun.CreateTUN()`（WireGuard 库）创建 utun 设备，涉及以下需要 root 权限的操作：

| 操作 | 函数/命令 | 需要 root |
|------|-----------|-----------|
| 创建 utun 设备 | `socket(PF_SYSTEM) + connect(SYSPROTO_CONTROL)` | ✅ |
| 启用 IP 转发 | `sysctl -w net.inet.ip.forwarding=1` | ✅ |
| 配置 IP 地址 | `/sbin/ifconfig utunX inet ...` | ✅ |
| 设置路由 | `pfroute-darwin.sh setup ...` | ✅ |
| pf 防火墙规则 | `pfctl` (通过 pfroute-darwin.sh) | ✅ |

**Makefile** (`fedbridge/Makefile:12-14`) 自动设置：

```makefile
Darwin)
    echo "==> macOS: run with sudo for utun device"
    sudo chown root:wheel $(BINARY) && sudo chmod u+s $(BINARY)
```

**代码** (`virtun.go:122-123`) 在创建失败时提示：

```go
log.Println(err, "recheck modprobe tun or root/cap_net_admin")
log.Println("    On MacOS, sudo chown root:wheel main && sudo chmod u+s main")
```

### u+s vs sudo 兼容性矩阵

| 运行方式 | `u+s` 状态 | utun 结果 | 说明 |
|----------|-----------|-----------|------|
| `./main` | 有 `u+s` | ✅ 正常 | setuid 提权到 root |
| `./main` | 无 `u+s` | ❌ 失败 | `Operation not permitted` |
| `sudo ./main` | 有 `u+s` | ✅ 正常 | sudo 提供 root，u+s 冗余但无害 |
| `sudo ./main` | 无 `u+s` | ✅ **正常** | sudo 提供 root，utun 无影响 |

**关键结论**: `sudo ./main` 在 `u+s` 移除后 utun 仍然正常工作。唯一变化是不能直接 `./main` 了。

---

## 变通方案

### 方案 A：`sudo bash -c` 包装

```bash
sudo bash -c './main -m 2>&1' | grep -a -i foo
```

原理：`2>&1` 在 `bash -c` 内部执行，sudo 只看到合并后的 stdout pipe，避免 sudo + fd 2 pipe 的交互问题。

### 方案 B：移除 u+s + sudo

```bash
sudo chmod u-s main
sudo ./main -m 2>&1 | grep -a -i foo
```

原理：去掉 setuid bit 后，sudo 以标准方式管理二进制，避免 setuid + sudo 双重复合状态。
**utun 仍然正常**，因为 `sudo` 提供 root。

### 方案 C：禁用 use_pty（如果确认是 PTY 导致）

```bash
sudo visudo
# 添加：
Defaults !use_pty
```

风险：降低对 CVE-2005-4890（tty 注入攻击）的防护。

### 方案 D：测试用 — 确认管道本身无问题

```bash
# 测试 sudo 与普通命令的 pipe
sudo echo hello | grep hello

# 测试二进制自身的输出（不用 grep）
sudo ./main -m > /tmp/out.txt && cat /tmp/out.txt

# 测试不同重定向顺序
sudo ./main -m 2>&1 | head -20
sudo ./main -m 2>/tmp/stderr.txt | head -20
```

---

## 参考链接

- sudo 项目 Issue #258 — *Please consider enabling option use_pty by default*: https://github.com/sudo-project/sudo/issues/258
- sudo 更新日志 — 1.9.14 use_pty 默认开启: https://www.sudo.ws/docs/upgrade/
- sudo 项目 Issue #338 — *use_pty should be off for root*: https://github.com/sudo-project/sudo/issues/338
- Apple Developer Forums #663632 — *Non-Master PTY output discarded on macOS*: https://developer.apple.com/forums/thread/663632
- Python Pexpect Issue #662 — *macOS: Slave PTY output discarded on child process exit*: https://github.com/pexpect/pexpect/issues/662
- Ruby Bug #20682 — *Slave PTY output is lost after a child process exits in macOS*: https://bugs.ruby-lang.org/issues/20682
- Jumpstarter PR #837 — *fix(hooks): replace PTY with subprocess.PIPE for reliable output capture*: https://github.com/jumpstarter-dev/jumpstarter/pull/837
- quick-lint-js pty.cpp — S_CTTYREF 详细分析: https://github.com/quick-lint/quick-lint-js/blob/master/src/quick-lint-js/port/pty.cpp#L205
- XNU 源码 — tty_tty.c S_CTTYREF: https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/tty_tty.c
- XNU 源码 — kern_exit.c ptsclose/ttywait: https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_exit.c
- CPython Issue #139412 — *sudo use_pty causes corrupted output*: https://github.com/python/cpython/issues/139412
