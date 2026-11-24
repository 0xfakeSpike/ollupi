# 事件流

引擎里所有状态变化都只走一条写路径：`store.Update` 事务。事务提交后，store 把这次提交改了什么算成事件，发到进程内的 `store.Feed` 上。订阅方把事件当作唤醒信号，回到 store 读出当前状态，再决定做什么。写的一方不通知任何人，也不直接调用任何订阅方。

会话本身用的是事件溯源：日志 `entries` 只追加，轮次和条目的状态都从日志折叠出来（见 [lifecycle.md](lifecycle.md)）。feed 上的事件不是这份日志的副本，只是“某次提交改了这些东西”的通知。

本页按当前代码写。代码在 [store/feed.go](../store/feed.go)、[store/sqlite/sqlite.go](../store/sqlite/sqlite.go)、[schedule/](../schedule/)、[run/](../run/)。

## 总览

```mermaid
flowchart LR
  subgraph W["写入方"]
    direction TB
    client["客户端请求<br/>经 gateway"]
    app["app<br/>Send · Steer · Decide · Answer · Interrupt<br/>会话和资源的增删改"]
    cron["cron.Run<br/>到点时提交到期的定时任务"]
    runner["run.Runner<br/>模型 · 工具 · 子 agent · 后台任务"]
    back["记忆抽取、用量记录<br/>回头写入"]
    client --> app
  end

  subgraph S["store：唯一写路径"]
    direction TB
    tx["store.Update 进写队列<br/>写协程逐个执行事务<br/>Append：Check → Apply → 物化<br/>Put / Delete 资源"]
    db[("SQLite<br/>entries · threads · turns · items<br/>tasks · cron_jobs · memories ……")]
    commit["提交成功<br/>tx.events() 算出事件<br/>整次提交共用一个 seq<br/>写协程按提交顺序放进事件队列"]
    feed{{"store.Feed<br/>事件队列 + 一个分发协程<br/>按入队顺序扇出"}}
    tx --> db
    tx --> commit --> feed
  end

  app --> tx
  cron -- "app.Scheduled" --> tx
  runner --> tx
  back --> tx
  runner -. "Delta：流式增量<br/>不进事务，没有 seq<br/>同样进事件队列" .-> feed

  subgraph R["订阅方：记下要做的事，唤醒自己"]
    direction TB
    gw["gateway 连接<br/>按用户订阅"]
    ts["线程调度器"]
    ks["任务调度器"]
    ex["记忆抽取"]
    us["用量记录"]
    mcp["MCP 连接"]
    cr["定时任务 cron.Run"]
  end

  feed --> gw & ts & ks & ex & us & mcp & cr

  subgraph O["随后：回到 store 读真相再行动"]
    direction TB
    o1(["通知客户端<br/>发审批、提问请求"])
    o2(["读 Runnable，起 Run<br/>Runner 又成为写入方"])
    o3(["读 Tasks，起 Runner.Task"])
    o4(["读结束的轮次，调模型<br/>写记忆"])
    o5(["读模型响应<br/>写用量行（不发事件）"])
    o6(["断开旧连接<br/>下次取工具时重连"])
    o7(["读最早的下次运行时间<br/>重新定闹钟"])
  end

  gw --> o1
  ts --> o2
  ks --> o3
  ex --> o4
  us --> o5
  mcp --> o6
  cr --> o7

  classDef writer fill:#fff4df,stroke:#c98b2e,color:#33230d
  classDef core fill:#e9f2ff,stroke:#3978b8,color:#102337
  classDef reader fill:#eaf7ed,stroke:#3c8b53,color:#102b17
  classDef action fill:#f4f4f4,stroke:#8a8a8a,color:#222
  class client,app,cron,runner,back writer
  class tx,db,commit,feed core
  class gw,ts,ks,ex,us,mcp,cr reader
  class o1,o2,o3,o4,o5,o6,o7 action
```

图里有三个环，它们让引擎自己转起来：

- **会话环**：Runner 写日志 → 提交 → 线程调度器听到会话可运行 → 读出可运行的会话 → 起 Run。审批、回答、子 agent 汇报、后台任务汇报都只是往日志里追加一条记录，会话因此变回可运行，调度器听到后接着跑。
- **任务环**：Runner 写 `TaskLaunched` → 任务调度器听到 → 起 `Runner.Task` → 写 `TaskReported` → 会话环接手。
- **记忆环**：轮次结束 → 记忆抽取把这个用户标脏 → 读结束的轮次、调模型 → 写记忆 → `memory/changed` 发给客户端。

## 一轮对话

从用户发消息到轮次结束，中间等一次审批。

```mermaid
sequenceDiagram
  autonumber
  participant C as 客户端
  participant G as gateway
  participant A as app
  participant S as store + feed
  participant Q as 线程调度器
  participant R as Runner
  participant M as 模型 / 工具

  C->>G: turn/start
  G->>A: Send
  A->>S: Update：TurnQueued + UserMessage
  S-->>G: Change(seq)
  G-->>C: thread/changed · turn/changed · item/changed
  S-->>Q: 会话头 Runnable → 唤醒
  Q->>S: Read：Runnable（跳过满额的用户）
  Q->>R: go Run(thread)

  loop 每一步：读 View，agent.Next 决定
    R->>S: Read：View
    alt CallModel
      R->>M: 流式请求
      M-->>R: 增量
      R-->>G: Delta（进事件队列，排在之前的提交后面）
      G-->>C: item/agentMessage/delta
      R->>S: Update：ModelResponded + AgentMessage + ToolCall / ApprovalAsked
      S-->>G: Change → item/changed；条目等审批时发 approval 请求
    else RunTools
      R->>S: Update：ToolStarted
      R->>M: Invoke（只读的工具并行）
      R->>S: Update：ToolResult 或 InputAsked
    end
  end
  Note over R: agent.Next 返回 Wait → Run 返回，槽位空出

  C->>G: 审批应答
  G->>A: Decide
  A->>S: Update：Decided
  S-->>Q: 又可运行 → 唤醒 → 再起 Run
  Note over R: RunTools → CallModel → …… → Finish
  R->>S: Update：TurnEnded
  S-->>Q: TurnEnded → 停掉这个会话的运行，唤醒排队的下一轮
  S-->>G: turn/changed(completed)
```

要点：

- Run 不在等待上阻塞。审批、回答、子 agent 汇报都在外面发生，Run 一遇到等待就返回，槽位让给别的会话。
- `Interrupt` 不去找正在跑的 Run。它只在事务里写 `TurnEnded`，调度器听到后取消这个会话的运行。`Steer` 也只是追加一条用户消息；正在等模型回答的 Run 发现期间来了新消息，就丢掉这次回答，重新读 View。
- 调度器自己出错或超时时，也通过写日志收尾：`Fail` 写 `TurnEnded(failed)`（会话），或写 `TaskReported(error)`（任务）。

## 子 agent 与后台任务

两者都是工具调用的异步分支。启动和汇报都是一次事务同时写两处，所以两边的事件共用一个 seq。

```mermaid
flowchart TB
  subgraph child["子 agent"]
    direction TB
    c1["父 Run：工具返回 Launch"]
    c2["一个事务<br/>CreateThread(子)<br/>子：TurnQueued + UserMessage<br/>父：ChildLaunched + ToolResult"]
    c3["线程调度器<br/>子会话可运行 → 起子 Run"]
    c4["子 Run 运行，直到 Finish"]
    c5["一个事务 run.End<br/>子：TurnEnded<br/>父：ChildReported"]
    c6["父会话不再等汇报 → 可运行<br/>线程调度器起父 Run"]
    c1 --> c2 --> c3 --> c4 --> c5 --> c6
  end

  subgraph task["后台任务"]
    direction TB
    k1["父 Run：工具声明在后台跑"]
    k2["一个事务<br/>CreateTask<br/>ToolStarted + TaskLaunched + ToolResult"]
    k3["任务调度器<br/>TaskLaunched → 读 Tasks → 起 Runner.Task"]
    k4["StartTask → Invoke"]
    k5["一个事务 ReportTask<br/>DeleteTask + TaskReported"]
    k6["会话可运行 → 线程调度器起 Run"]
    k1 --> k2 --> k3 --> k4 --> k5 --> k6
  end

  halt["父轮次提前结束（失败或被打断）<br/>同一事务里 run.End 递归：<br/>还在跑的子会话写 TurnEnded(interrupted)<br/>还在跑的任务写 TaskReported(error)"]
  halt -. "TurnEnded → 线程调度器取消子 Run" .-> c4
  halt -. "TaskReported → 任务调度器取消任务" .-> k4
```

## 谁听什么

| 提交里出现的 | 订阅方 | 反应 |
|---|---|---|
| 任何 `Change` | gateway | `thread/changed`，加上变了的 `turn/changed`、`item/changed`，都带 seq；条目在等审批或回答时，向客户端发请求 |
| `Change` 的会话头 `Runnable` 为真 | 线程调度器 | 唤醒，读 `Runnable` 补满槽位 |
| `Change` 含 `TurnEnded`，或会话被删 | 线程调度器 | 取消这个会话正在跑的 Run |
| `Change` 含 `TaskLaunched` | 任务调度器 | 唤醒，读 `Tasks` 补满槽位 |
| `Change` 含 `TaskReported`，或会话被删 | 任务调度器 | 取消对应的任务 |
| `Change` 含 `TurnEnded` | 记忆抽取 | 这个用户标脏，稍后读它结束的轮次 |
| `Change` 含 `ModelResponded` | 用量记录 | 这个会话标脏，稍后补上用量行 |
| `Owned.MCPServer` / `MCPServerDeleted` | MCP 连接 | 关掉这个用户的连接，下次取工具时重连 |
| `Owned.CronJob` / `CronJobDeleted` | 定时任务 | 醒来提交到期的任务，再按最早的下次运行时间重定闹钟 |
| 其他 `Owned` | gateway | `settings/changed`、`workspace/changed`、`environment/changed`、`mcpServer/changed`、`cron/changed`、`memory/changed`、`todo/changed` |
| `Delta` | gateway | `item/agentMessage/delta`、`item/reasoning/textDelta` |

`Change` 来自会话的写入：`Append`、`CreateThread`、`UpdateThread`、`DeleteThread`。`Owned` 来自用户资源的写入：设置、工作区、执行环境、MCP 服务器、定时任务、记忆、待办。`EnsureUser`、`SetMemoryState`、`PutUsage`、`PutPresence`、`CreateTask`、`StartTask`、`DeleteTask` 不发事件。后台任务之所以可见，是因为同一事务里追加了 `TaskLaunched`、`TaskReported` 这些条目。

## 约定

- **一个命令的改动在一个事务里提交。** 订阅方看到的每次提交都是完整的一步，不会看到做了一半的状态。比如中枢把工作交出去（`app.HandOff`）：新会话、第一条消息、待办的链接在同一次提交里出现，被拒绝时一样都不留下。
- **事件只是唤醒信号，真相在 store。** 订阅方不靠事件内容做决定：调度器读 `Runnable` / `Tasks`，抽取读 `EndedTurns`，用量读 `MissingUsage`。所以漏一个、多一个事件都无害。进程启动时，每个订阅方各扫一遍：调度器先唤醒自己一次，抽取把所有用户标脏，用量从头补。
- **事件按 seq 的顺序发出，靠队列而不是锁。** `store.Update` 把事务放进写队列，由一个写协程逐个执行；它提交一次，就把这次的事件放进 `store.Feed` 的事件队列，由一个分发协程按入队顺序调用订阅函数。只有写协程往里放提交的事件，所以事件天然按 seq 排好。写协程放完就去执行下一个事务，不等订阅函数；`Update` 自己要等到这次的事件发完才返回，所以调用方返回后，订阅方（比如客户端的出站队列）已经收到了。读走另外几个只读连接（WAL 下各读各的快照），不进写队列。
- **订阅函数不能阻塞。** 所有订阅函数都在同一个分发协程上调用：一个卡住，后面的事件都发不出去；事件队列积满后写协程也会停下。订阅函数只能记下要做的事、唤醒自己的循环，不能在里面调 `store.Update`（它要等这个分发协程，会死锁）。gateway 的出站队列有上限，客户端读得太慢就断开它。
- **Delta 不落库，但和提交排在同一个事件队列里。** 它没有 seq，断线期间的增量会丢，重连后用 `thread/read` 拿完整条目；在线时，它不会抢在之前的提交前面发出。
- **Delta 带 `offset`，客户端按它拼，`item/changed` 说了算。** `offset` 是这段增量在条目最终文本里的起点，单位是 UTF-16 码元（和 JS 字符串的 `length` 一致）。客户端的规则：
  - `offset` 不超过本地已有长度时，截到 `offset` 再接上这段；
  - `offset` 超过本地长度，说明中间漏了，丢掉这段，等 `item/changed`；
  - 收到这个条目的 `item/changed` 后，以其中的完整文本为准，之后再来的增量全部丢掉。

  这样重复、迟到、重连后重读都不会拼错。一次模型回调里同时有思考和正文时，会发成两条增量，各带各的 `offset`。
- **请求和响应不走 feed。** `thread/list`、`thread/read` 这类读接口直接读 store；`error` 通知由 gateway 直接发。
- **模型目录不在 store 里，也不发事件。** 部署的模型目录在 host 内存里，`Host.SetModels` 原地替换它，不经过事务；已经在调模型的轮次用原来的客户端跑完。用户在目录里选哪个模型开新会话、哪个模型学记忆，存在设置里（`Settings.Model`、`Settings.LearnModel`），改动照常经事务提交，以 `settings/changed` 通知客户端。记忆抽取每次处理一个用户时都重新读它的设置，不需要监听设置的变化。
- **只有这几处由时间驱动：** 定时任务按最早的下次运行时间定闹钟，最长一分钟醒一次核对时钟（Go 的计时器不计电脑睡眠的时间，归档的会话恢复时也没有定时任务事件）；抽取和用量失败后定时重试；shard 定时续租；wshub 定时 ping。
