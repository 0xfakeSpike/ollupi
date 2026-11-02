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

闭环负载下，1536 个活跃会话时 turn 耗时 p50 较单独运行增加 5%，2048 个时增加 30%。4096 个会话时吞吐为每秒 350 次模型调用，CPU 占用约 2 核，瓶颈为单个 SQLite 连接。

![突发与额度](docs/images/bench-burst.svg)

3000 个 turn 同时提交，在途请求始终不超过额度，未出现 429，完成耗时为理论下限的 1.03 倍。

![恢复](docs/images/bench-recovery.svg)

混合负载（工具调用、向用户提问、派生子代理）下随机 `kill -9` 共 90 次，960 个 turn 全部完成，无工具调用重复执行，被中断的调用均标记为可能已生效；为使中断覆盖各执行阶段，此项将模型调用缩短为 200 ms、工具调用缩短为 300 ms。持有 1 万个挂起会话与 500 个运行中 turn 时，重启耗时 448 ms。闲置会话被唤醒至发出模型请求的耗时随历史长度增长，1000 轮历史时 p50 为 73 ms。

复现：

```bash
go run -tags sqlite_fts5 ./bench parked -n 50000 -state waiting -latency 5s -rounds 3
go run -tags sqlite_fts5 ./bench isolated -k 20
go run -tags sqlite_fts5 ./bench steady -c 1536
go run -tags sqlite_fts5 ./bench burst -n 3000 -slots 512 -calls 256 -latency 5s -jitter 2s -tool 1s
go run -tags sqlite_fts5 ./bench resume
go run -tags sqlite_fts5 ./bench crash -n 500 -kills 30
```

## 执行模型

![共享执行池](docs/images/execution-pool.svg)

所有会话中可推进的 turn，无论来自用户、子代理还是定时任务，进入同一队列，由固定数量的槽位（默认 32 个）执行。turn 等待用户、审批或子代理时释放槽位，不占用 goroutine 与模型并发额度，等待结束后重新就绪。槽位不足时，普通会话先于子代理，同类按就绪先后执行。模型并发按连接限制（默认每个连接 4 个），工具执行不占用模型额度。

每一步先写入数据库再执行，调度状态不驻留内存，进程重启后由同一调度逻辑恢复进行中的 turn。工具调用区分 `pending`（未执行）与 `inProgress`（可能已生效）：恢复时前者照常执行，后者标记为失败并提示核对，不会重复执行。

## 许可

[MIT](LICENSE)
