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

以下结果使用模拟的模型与工具，测量调度与持久化，不涉及模型质量。方法、完整数据与复现命令见 [bench](bench/README.md)。

![与 pi、DeepSeek Harness 的对比](docs/images/bench-compare.svg)

![内存](docs/images/bench-memory.svg)

![容量](docs/images/bench-capacity.svg)

![突发与额度](docs/images/bench-burst.svg)

![恢复](docs/images/bench-recovery.svg)

![混合负载](docs/images/bench-mix.svg)

![故障与长尾](docs/images/bench-faults.svg)

![长时间运行](docs/images/bench-soak.svg)

## 执行模型

![共享执行池](docs/images/execution-pool.svg)

所有会话中可推进的 turn，无论来自用户、子代理还是定时任务，进入同一队列，由固定数量的槽位（默认 32 个）执行。turn 等待用户、审批或子代理时释放槽位，不占用 goroutine 与模型并发额度，等待结束后重新就绪。槽位不足时，普通会话先于子代理，同类按就绪先后执行。模型并发按连接限制（默认每个连接 4 个），工具执行不占用模型额度。模型请求遇到 429、5xx 或连接中断时按退避重试，最多 8 次，等待期间保留槽位与模型额度。

每一步先写入数据库再执行，调度状态不驻留内存，进程重启后由同一调度逻辑恢复进行中的 turn。工具调用区分 `pending`（未执行）与 `inProgress`（可能已生效）：恢复时前者照常执行，后者标记为失败并提示核对，不会重复执行。

## 许可

[MIT](LICENSE)
