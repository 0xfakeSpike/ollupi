<h1>
  <img alt="Ollupi" src="docs/logo/logo.svg" width="240">
</h1>

Ollupi 是一个 Go agent 框架，供云服务商构建大规模并发的个人 agent 服务。

Ollupi 不以虚拟机或容器逐个隔离 agent，而是池化 runtime，并将 agent 的运行状态持久化：

- **任意时刻可恢复。** 每一步先写入数据库再执行，调度状态不驻留内存。发版、重启或崩溃后，会话从中断处继续。
- **不活跃的会话不占资源。** 等待用户、审批或子代理的会话退出执行池，仅保留数据库记录，不占用内存与计算。
- **并发受控。** 执行池按槽位调度，模型调用按连接限流，与服务商的账户额度对应；突发请求排队处理。

会话、子代理和定时任务共用同一个执行池。[ollupi-desktop](https://github.com/0xfakeSpike/ollupi-desktop) 是基于 Ollupi 的桌面端。

![Ollupi 架构](docs/images/architecture.svg)

## 与逐个隔离 agent 的区别

| | 每个 agent 一个虚拟机或容器 | Ollupi |
|---|---|---|
| 一个进程 | 一个会话 | 所有会话 |
| 不活跃的会话 | 进程常驻，或冷启动重建 | 只有数据库记录 |
| 等待用户、审批时 | 进程挂起，仍占内存 | 退出执行池，不占资源 |
| 调度状态 | 进程内存 | SQLite |
| 重启后 | 进行中的任务中断 | 从断点继续 |
| 子代理 | 特殊机制或子进程 | 普通会话，同池排队 |
| 定时任务 | 外部 cron 唤起进程 | 到点向会话发送消息，同池排队 |

![逐个隔离 agent 与 Ollupi 的对比](docs/images/cli-vs-ollupi.svg)

## 性能实测

测试使用模拟的模型与工具：模拟服务商经 OpenAI 客户端访问，在途请求超出额度时返回 429。除另行说明外，单次模型调用 5 s ± 2 s，单次工具调用 1 s，每个 turn 含 3 次模型调用与 2 次工具调用，单独运行约 17 s。测试环境为 Apple M1（8 核，8 GB），单个 SQLite 数据库。测量对象为调度与持久化，不涉及模型质量。

![内存](docs/images/bench-memory.svg)

挂起会话不驻留内存：5 万个挂起会话使常驻内存增加约 14 MB（含测试程序自身的记录），数据库占用约 2.3 KB/会话，同进程内活跃 turn 的延迟不受影响（p50 由 5.04 s 变为 5.05 s）。活跃会话约 160 KB/会话。对照组为每个会话一个 Ollupi 进程，20.0 MB/会话，未计容器开销。

![容量](docs/images/bench-capacity.svg)

闭环负载下，2048 个活跃会话时 turn 耗时 p50 较单独运行增加 2%，3072 个时增加 6%，4096 个时增加 23%。4096 个会话时吞吐为每秒 477 次模型调用，CPU 占用约 1.7 核。每个 turn 执行 68 次读、15 次写，等待单个 SQLite 连接合计 6 ms（2048 个会话）至 43 ms（4096 个）。

![突发与额度](docs/images/bench-burst.svg)

3000 个 turn 同时提交，在途请求始终不超过额度，未出现 429，完成耗时为理论下限的 1.03 倍。

![恢复](docs/images/bench-recovery.svg)

混合负载（工具调用、向用户提问、派生子代理）下随机 `kill -9` 共 90 次，960 个 turn 全部完成，无工具调用重复执行，被中断的调用均标记为可能已生效；为使中断覆盖各执行阶段，此项将模型调用缩短为 200 ms、工具调用缩短为 300 ms。持有 1 万个挂起会话与 500 个运行中 turn 时，重启耗时 448 ms。闲置会话被唤醒至发出模型请求的耗时随历史长度增长，1000 轮历史时 p50 为 73 ms。

256 个会话持续发送 turn（其中 20% 交给子代理），运行中 1000 个定时任务同时到点：高峰之外，turn 等待首次模型请求的 p50 为 7 ms、p99 为 274 ms；高峰期间新到的 turn 与定时任务按就绪先后排队，p50 为 44.1 s，高峰在 59.4 s 内排空，与模型额度允许的下限相当。子代理等待的 p99 为 4.6 s，各会话 turn 耗时的 Jain 公平性指数为 0.978。

64 个 turn 各同时派生 8 个子代理，共 512 个子代理而只有 32 个槽位：576 个 turn 全部完成，耗时为理论下限的 1.11 倍，等待子代理的父 turn 均让出了槽位。

5% 的模型请求返回 500、同账户其他客户端占用最多 256 个额度时，1024 个会话的 3072 个 turn 无一失败，p99 由 21.6 s 增至 28.5 s。模型延迟改为对数正态分布（中位数 5 s，σ = 0.6）时，1536 个会话的 4608 个 turn 无一失败，p50 为 19.0 s、p99 为 42.4 s。

256 个会话连续运行 8 分钟（模型调用 1 s，共 3.5 万个 turn）：goroutine 稳定在约 1000 个，GC 停顿的 p99 不超过 0.23 ms，WAL 稳定在 6.1 MB，数据库每个 turn 增长约 3.2 KB，turn 耗时 p50 由 3.44 s 增至 3.60 s。会话历史越长，CPU 与内存越高：历史由 8 轮增至 138 轮，每个 turn 的 CPU 由 12.1 ms 增至 34.8 ms，主要用于编码含完整历史的请求和每个 turn 重读历史；常驻内存由 99 MB 增至 396 MB。

与 pi、DeepSeek Harness 在同一模拟服务商与负载下对比（各测一次；内存按 phys_footprint 计，Ollupi 按常驻内存计）：

| | Ollupi | pi（SDK） | pi-durable | DeepSeek Harness |
|---|---|---|---|---|
| 每个挂起会话的内存 | 约 0.3 KB | 91 KB | 23 KB | 181 KB |
| 2048 个活跃会话时 p50 增幅 | 2% | 2% | 314% | 77% |
| 3000 个 turn 同时提交、额度 256 | 全部完成 | 2534 个失败 | 2181 个失败 | 2190 个失败 |
| 运行中 `kill -9` 后 | 全部续跑，工具不重复执行 | 进行中的 turn 丢失 | 全部续跑，工具不重复执行 | turn 标为中断 |

版本为 pi-coding-agent 1.4.0、pi-durable 1.4.0（实验性）、DeepSeek Harness 0.2.1-rc.5，方法与原始结果见 [bench/compare](bench/compare/README.md)。

复现：

```bash
go run -tags sqlite_fts5 ./bench parked -n 50000 -state waiting -latency 5s -rounds 3
go run -tags sqlite_fts5 ./bench isolated -k 20
go run -tags sqlite_fts5 ./bench steady -c 3072
go run -tags sqlite_fts5 ./bench burst -n 3000 -slots 512 -calls 256 -latency 5s -jitter 2s -tool 1s
go run -tags sqlite_fts5 ./bench resume
go run -tags sqlite_fts5 ./bench crash -n 500 -kills 30
go run -tags sqlite_fts5 ./bench mix
go run -tags sqlite_fts5 ./bench fanout
go run -tags sqlite_fts5 ./bench steady -c 1024 -fail 0.05 -others 256
go run -tags sqlite_fts5 ./bench steady -c 1536 -sigma 0.6
go run -tags sqlite_fts5 ./bench soak -c 256 -d 8m -every 30s -latency 1s -jitter 200ms -tool 200ms
```

## 执行模型

![共享执行池](docs/images/execution-pool.svg)

所有会话中可推进的 turn，无论来自用户、子代理还是定时任务，进入同一队列，由固定数量的槽位（默认 32 个）执行。turn 等待用户、审批或子代理时释放槽位，不占用 goroutine 与模型并发额度，等待结束后重新就绪。槽位不足时，普通会话先于子代理，同类按就绪先后执行。模型并发按连接限制（默认每个连接 4 个），工具执行不占用模型额度。模型请求遇到 429、5xx 或连接中断时按退避重试，最多 8 次，等待期间保留槽位与模型额度。

每一步先写入数据库再执行，调度状态不驻留内存，进程重启后由同一调度逻辑恢复进行中的 turn。工具调用区分 `pending`（未执行）与 `inProgress`（可能已生效）：恢复时前者照常执行，后者标记为失败并提示核对，不会重复执行。

## 许可

[MIT](LICENSE)
