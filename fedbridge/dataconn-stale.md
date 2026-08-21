

### 数据断流问题说明

从连接runSoftunPhyport开始, 以http下载功能作为测试,

只有首次连接的carria stream有效, 如果首次连接因网络异常断开,

切换到新的carria stream则进入stale状态,再也无法恢复数据流,可能长达10分钟

偶尔能看到1,2次的丢失包日志.

切换重连接carrria stream耗时约10秒.

### 涉及模块

* iptunnel
* softun

### 涉及文档

* dataflow.md

### 相关日志
