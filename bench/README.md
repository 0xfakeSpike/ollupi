# Bench

测量 Ollupi 的调度与持久化，不涉及模型质量。模型与工具均为模拟：模拟服务商经 OpenAI 客户端访问，在途请求超出额度时返回 429。除另行说明外，单次模型调用 5 s ± 2 s，单次工具调用 1 s，每个 turn 含 3 次模型调用与 2 次工具调用，单独运行约 17 s。测试环境为 Apple M1（8 核，8 GB），单个 SQLite 数据库。

README 中的图由 [docs/images/figures.py](../docs/images/figures.py) 绘制。原始输出保存在 [results](results)，与其他 harness 的对比在 [compare](compare/README.md)。

## 运行

测量时机器上不应有其他负载，否则延迟类数字不可信：

- 一次只跑一项，不同实验、不同项目不要同时运行。
- 先退出 Docker Desktop 等虚拟机，确认没有其他项目在跑测试，CPU 空闲至少 60%。
- 保持盖子打开。合盖后系统睡眠，`caffeinate` 拦不住；运行期间睡眠过的结果要作废。

```bash
go run -tags sqlite_fts5 ./bench parked -n 50000 -state waiting -latency 5s -rounds 3
go run -tags sqlite_fts5 ./bench isolated -k 20
go run -tags sqlite_fts5 ./bench steady -c 3072
go run -tags sqlite_fts5 ./bench steady -c 1024 -fail 0.05 -others 256
go run -tags sqlite_fts5 ./bench steady -c 1536 -sigma 0.6
go run -tags sqlite_fts5 ./bench burst -n 3000 -slots 512 -calls 256 -latency 5s -jitter 2s -tool 1s
go run -tags sqlite_fts5 ./bench resume
go run -tags sqlite_fts5 ./bench crash -n 500 -kills 30
go run -tags sqlite_fts5 ./bench mix
go run -tags sqlite_fts5 ./bench fanout
go run -tags sqlite_fts5 ./bench soak -c 256 -d 8m -every 30s -latency 1s -jitter 200ms -tool 200ms
```

| 实验 | 测什么 | 原始输出 |
|---|---|---|
| `parked` | 挂起会话的内存与磁盘，以及对活跃 turn 的影响 | 未保存 |
| `isolated` | 对照组：每个会话一个进程 | 未保存 |
| `steady` | 闭环负载下的容量；`-fail`、`-others`、`-sigma` 注入故障与长尾 | `results/steady-*.txt` |
| `burst` | 一次提交大量 turn 时的排队与额度 | `results/burst.csv`（时间线） |
| `resume` | 闲置会话被唤醒至发出模型请求的耗时 | 未保存 |
| `crash` | 随机 `kill -9` 后的恢复 | 未保存 |
| `mix` | 用户、子代理与定时任务混合时的等待与公平性 | `results/mix.txt` |
| `fanout` | 子代理数超过槽位时父 turn 是否让出槽位 | `results/fanout.txt` |
| `soak` | 长时间运行时的耗时、内存、goroutine、GC 与数据库增长 | `results/soak.csv` |

`results` 中的数字于 2026-11-04 测得，每项跑一次。`parked`、`isolated`、`burst`、`resume`、`crash` 是更早的结果，此后未重测。

## 结果

### 内存

挂起会话不驻留内存：5 万个挂起会话使常驻内存增加约 14 MB（含测试程序自身的记录），数据库占用约 2.3 KB/会话，同进程内活跃 turn 的延迟不受影响（p50 由 5.04 s 变为 5.05 s）。活跃会话约 160 KB/会话。对照组为每个会话一个 Ollupi 进程，20.0 MB/会话，未计容器开销。

### 容量

| 活跃会话 | p50 | p95 | p99 | 模型调用/秒 | CPU（核） | 常驻内存 | 等待连接/turn |
|---|---|---|---|---|---|---|---|
| 512 | 17.1 s | 20.4 s | 21.4 s | 72 | 0.39 | 132 MB | 2.9 ms |
| 1024 | 17.2 s | 20.5 s | 21.6 s | 147 | 0.60 | 223 MB | 4.5 ms |
| 1536 | 17.3 s | 20.7 s | 21.9 s | 219 | 0.81 | 315 MB | 15.2 ms |
| 2048 | 17.3 s | 20.8 s | 22.1 s | 296 | 1.03 | 407 MB | 6.3 ms |
| 3072 | 18.0 s | 22.5 s | 24.0 s | 416 | 1.39 | 570 MB | 22.8 ms |
| 4096 | 20.9 s | 26.1 s | 27.7 s | 477 | 1.67 | 692 MB | 43.0 ms |

每个 turn 执行 68 次读、15 次写，磁盘占用约 11.6 KB。4096 个会话时 turn 比单独运行慢 3.9 s，而等待 SQLite 连接合计只有 43 ms，瓶颈尚未查明。

### 突发

3000 个 turn 同时提交，在途请求始终不超过额度，未出现 429，完成耗时为理论下限的 1.03 倍。

### 恢复

混合负载（工具调用、向用户提问、派生子代理）下随机 `kill -9` 共 90 次，960 个 turn 全部完成，无工具调用重复执行，被中断的调用均标记为可能已生效；为使中断覆盖各执行阶段，此项将模型调用缩短为 200 ms、工具调用缩短为 300 ms。持有 1 万个挂起会话与 500 个运行中 turn 时，重启耗时 448 ms。闲置会话被唤醒至发出模型请求的耗时随历史长度增长，1000 轮历史时 p50 为 73 ms。

### 混合负载

256 个会话持续发送 turn（其中 20% 交给子代理），运行中 1000 个定时任务同时到点：高峰之外，turn 等待首次模型请求的 p50 为 7 ms、p99 为 274 ms；高峰期间新到的 turn 与定时任务按就绪先后排队，p50 为 44.1 s，高峰在 59.4 s 内排空，与模型额度允许的下限相当。子代理等待的 p99 为 4.6 s，各会话 turn 耗时的 Jain 公平性指数为 0.978。

### 子代理扇出

64 个 turn 各同时派生 8 个子代理，共 512 个子代理而只有 32 个槽位：576 个 turn 全部完成，耗时为理论下限的 1.11 倍，等待子代理的父 turn 均让出了槽位。

### 故障与长尾

5% 的模型请求返回 500、同账户其他客户端占用最多 256 个额度时，1024 个会话的 3072 个 turn 无一失败，p99 由 21.6 s 增至 28.5 s。加入退避重试之前，同样的负载有 326 个 turn 失败（这一次未保存原始输出）。模型延迟改为对数正态分布（中位数 5 s，σ = 0.6）时，1536 个会话的 4608 个 turn 无一失败，p50 为 19.0 s、p99 为 42.4 s。

### 长时间运行

256 个会话连续运行 8 分钟（模型调用 1 s，共 3.5 万个 turn）：goroutine 稳定在约 1000 个，GC 停顿的 p99 不超过 0.23 ms，WAL 稳定在 6.1 MB，数据库每个 turn 增长约 3.2 KB，turn 耗时 p50 由 3.44 s 增至 3.60 s。会话历史越长，CPU 与内存越高：历史由 8 轮增至 138 轮，每个 turn 的 CPU 由 12.1 ms 增至 34.8 ms，主要用于编码含完整历史的请求和每个 turn 重读历史；常驻内存由 99 MB 增至 396 MB。

### 与其他 harness 对比

同一模拟服务商与负载，各测一次；内存按 phys_footprint 计，Ollupi 按常驻内存计。

| | Ollupi | pi（SDK） | pi-durable | DeepSeek Harness |
|---|---|---|---|---|
| 每个挂起会话的内存 | 约 0.3 KB | 91 KB | 23 KB | 181 KB |
| 2048 个活跃会话时 p50 增幅 | 2% | 2% | 314% | 77% |
| 3000 个 turn 同时提交、额度 256 | 全部完成 | 2534 个失败 | 2181 个失败 | 2190 个失败 |
| 运行中 `kill -9` 后 | 全部续跑，工具不重复执行 | 进行中的 turn 丢失 | 全部续跑，工具不重复执行 | turn 标为中断 |

版本为 pi-coding-agent 1.4.0、pi-durable 1.4.0（实验性）、DeepSeek Harness 0.2.1-rc.5，方法与原始结果见 [compare](compare/README.md)。
