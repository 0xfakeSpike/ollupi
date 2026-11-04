# 与其他 harness 的对比测试

用 ollupi `bench` 的同一负载测量两个 Node.js agent harness，结果可与 [bench](../README.md) 的数字并列：

- **pi**：`@earendil-works/pi-coding-agent` 1.4.0（`pi-sdk`，SDK 的 `AgentSession`，每会话一个 JSONL 文件），以及实验性的 `@earendil-works/pi-durable` 1.4.0（`pi-durable`，一个 `Harness` 托管多个会话，单个 SQLite 文件）。
- **dsh**：`@deepseek-ai/dsh` 0.2.1-rc.5（DeepSeek Harness，Cordis 插件框架）。使用 `sdk-minimal` profile：一个进程经 stdio JSON-RPC 托管多个会话，会话以 JSONL 存于 `$DSH_HOME/sessions`。

模型一律为 ollupi 自带的模拟服务商（`bench provider`，单独进程，OpenAI 兼容的流式 chat completions，在途超额返回 429）；每个 turn 先两次调用工具 `bench__work`（耗时 1 s，返回 "ok"），再给出文本，单独运行约 17 s。不使用任何真实 API key；`PI_OFFLINE=1`；不读写 `~/.pi`、`~/.dsh`（配置都在 `run/` 下）。

## 准备

```bash
cd bench/compare
npm install --ignore-scripts
(cd ../.. && go build -tags sqlite_fts5 -o bench/compare/run/bench ./bench)   # 模拟服务商
```

Node 22.19 以上（测试用 v22.23）。

## 复现

全部实验（约 1 小时，结果追加到 `results/results.txt`）：

```bash
./run-all.sh
```

单项（`--harness` 取 `pi-sdk`、`pi-durable`、`dsh`）：

```bash
node --expose-gc parked.mjs  --harness pi-durable --n 1000                 # 1. 挂起会话的内存与磁盘，丢弃后从磁盘恢复的耗时
node --expose-gc parked.mjs  --harness pi-durable --n 1000 --retention 0   #    pi-durable 不在内存保留闲置会话
node --expose-gc steady.mjs  --harness pi-sdk --c 1024                     # 2. c 个会话各连续 3 个 turn，服务商额度 = c
node --expose-gc burst.mjs   --harness dsh --n 3000 --limit 256            # 3. 3000 个 turn 同时提交，服务商额度 256
node crash.mjs     --n 50 --turns 20 --kills 30                            # 4. pi-durable：随机 kill -9 后重启续跑
node crash-sdk.mjs --n 200 --turns 5 --at 1500                             #    pi-sdk：kill -9 一次后会话文件的状态
node crash-dsh.mjs --mode closed --n 50 --turns 20 --kills 30              #    dsh：随机 kill -9，重启后 session/resume
node crash-dsh.mjs --mode queued --n 50 --turns 20 --kills 30              #    dsh：预先排队的 prompt 在崩溃后的去向
node isolated-dsh.mjs --k 10                                               # 5. 每会话一个 dsh 进程的内存基线
```

`locked.sh` 用于与 ollupi 自身的 bench 错开运行：`.locks/gobench.lock` 存在时等待，运行期间持有 `.locks/compare.lock`。

## 文件

| 文件 | 内容 |
|---|---|
| `common.mjs` | 启动模拟服务商、内存（RSS 与 macOS phys_footprint）、分位数、结果记录 |
| `harnesses.mjs` | 三个 harness 的统一接口 |
| `pi-sdk.mjs` | pi-coding-agent：`models.json` 中的 openai-completions 服务商、`bench__work` 工具、持久化 `SessionManager` |
| `pi-durable.mjs` | pi-durable：`createProvider` 指向模拟服务商、`bench__work` 扩展、`openNodeSqliteStorage` |
| `dsh/patch.yml`、`dsh/bench-tool.mjs`、`dsh/client.mjs` | dsh：profile 覆盖层（pi-ai 适配器指向模拟服务商、关闭 shell 工具、加入 `bench__work` 插件）与 JSON-RPC 客户端 |
| `parked.mjs`、`steady.mjs`、`burst.mjs`、`crash*.mjs`、`isolated-dsh.mjs` | 各实验 |
| `results/results.txt` | 原始输出 |

## 注意

- pi 两种 harness 在测试进程内运行，内存在强制 GC 后读取；dsh 为独立进程，用 `ps` 与 `footprint` 读取。
- 测试机内存紧张时 macOS 会压缩页面，`ps` 的 RSS 偏低；`footprint`（phys_footprint）计入被压缩的页面，以它为准。
- pi 与 dsh 都没有对模型调用的并发限制，突发测试中的 429 由各自的重试策略处理。

## 结果（2026-11-02，Apple M1 8 核 8 GB，每项单次运行）

原始输出见 `results/results.txt`。`results/run-all.log` 中第一次 dsh 突发结果（`failed_turns=0`）有误：当时客户端把 `session/wait` 返回即视为成功，而 dsh 无论 turn 如何结束都会返回；修正为读取 `turn/end` 的 `reason` 后重跑，结果见 `results/extra.log`。dsh 闭环负载各次的请求数恰为 turn 数的 3 倍且无 429，不受此影响。内存一律取 phys_footprint。

**1. 挂起会话**（每个会话跑过一个 turn 后闲置）

| | 会话数 | 内存/会话 | 磁盘/会话 | 闲置会话能否移出内存 | 从磁盘恢复 |
|---|---|---|---|---|---|
| pi-sdk | 10000 | 91 KB（JS 堆 25 KB） | 3.2 KB（JSONL） | 能，`dispose()` 后从文件重开 | 0.8 ms |
| pi-durable | 10000 | 23 KB（JS 堆 12.5 KB，默认在内存保留 10 分钟） | 8.5 KB（SQLite） | 句柄无状态，按保留时长释放 | 0.4 ms（重开 Harness 6 ms） |
| dsh | 5000 | 181 KB | 8.3 KB（JSONL） | SDK 协议无关闭会话的方法，只能重启进程 | 重启进程 0.9 s，`session/resume` 9.6 ms |
| ollupi | 50000 | ≈0.3 KB | 2.3 KB | 不驻留 | — |

pi-durable 建立 1 万个会话耗时 551 s，而 1000 个只需 14 s，吞吐随会话数下降。

**2. 闭环负载**（每会话连续 3 个 turn，额度 = c；表中为 turn 耗时 p50 相对单独运行 17 s 的增幅）

| c | pi-sdk | pi-durable | dsh | ollupi |
|---|---|---|---|---|
| 64 | +1% | −1% | +2% | |
| 256 | +1% | +1% | +2% | |
| 1024 | +1% | +19% | +15% | |
| 1536 | | | | +2% |
| 2048 | +2%（p99 22 s） | +314%（p50 70 s） | +77%（p50 30 s） | +2% |

c = 2048 时峰值内存：pi-sdk 604 MB（230 KB/会话），pi-durable 733 MB（318 KB/会话），dsh 1414 MB（659 KB/会话）；ollupi 活跃会话约 160 KB/会话。CPU：pi-sdk 0.46 核，pi-durable 1.00 核，dsh 0.86 核（Node 单线程，接近 1 核即饱和）。每 turn 磁盘：pi-sdk 2.6 KB，pi-durable 6.2 KB（关闭后；运行中 WAL 更大），dsh 6.8 KB。

**3. 突发**（3000 个 turn 同时提交，额度 256，理论下限 176 s）

| | 完成 | 失败 | 429 次数 | 耗时 |
|---|---|---|---|---|
| pi-sdk | 466 | 2534 | 10953 | 39 s |
| pi-durable | 819 | 2181 | 10680 | 98 s |
| dsh | 810 | 2190 | 19537 | 93 s |
| ollupi | 3000 | 0 | 0 | 下限的 1.03 倍 |

三者均无模型调用并发限制，只有重试（pi 两者默认 3 次、2 s 起指数退避；dsh 默认 5 次、500 ms 至 10 s），重试用尽的 turn 以错误结束。

**4. 崩溃**（模型 200 ms，工具 300 ms，随机 0.3–2 s 后 `kill -9`）

| | 负载 | kill 次数 | turn 结果 | 工具重复执行 | 重启 |
|---|---|---|---|---|---|
| pi-durable | 50 会话 × 20 个排队 turn | 27 | 1000 个全部完成；683 次被中断的工具调用以 interrupted 错误交给模型，不重跑 | 0 | 212 ms（p50） |
| pi-sdk | 200 会话 × 5 turn | 1 | 进行中的 200 个 turn 停在半途，重开后不会继续；新 prompt 可正常运行 | 0（不重跑） | — |
| dsh（逐个发送） | 50 会话 × 20 turn | 18 | 874 个以 interrupted 结束，113 个完成；恢复时不续跑被中断的 turn | 按调用 ID 为 0；但 1247 次工具执行中 228 次在会话日志里没有对应调用 | 1.0 s（含 50 个 `session/resume`） |
| dsh（预先排队） | 50 会话 × 20 turn | 1 | 恢复时 900 个排队 prompt 被取消 | 同上 | 0.9 s |
| ollupi | 960 turn | 90 | 全部完成，被中断的调用标记为可能已生效 | 0 | 448 ms（1 万挂起 + 500 运行中） |

**5. 每会话一个 dsh 进程**：10 个进程平均 107 MB/进程（ollupi 为 20 MB/进程）。
