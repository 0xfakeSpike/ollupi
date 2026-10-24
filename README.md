# Ollupi

Ollupi 是一个 Go 写的 agent 运行时库：ReAct 循环、会话与调度、工具注册、权限与审批、沙箱、脱敏、记忆和网关协议。它负责把模型、工具、记忆、权限和沙箱组织成一条每一步都先落库、可以完整审计的执行链路。任何人都可以 import 它，接上自己的工具、技能和界面，做出自己的 agent 软件。

[haoagent](https://github.com/0xfakeSpike/haoagent) 是用 Ollupi 构建的参考应用：macOS 桌面端、Shell / MCP / OpenCLI / Skills 工具、语音外呼调查等都在那里。

## 核心机制

下面按独特程度排列，是 Ollupi 和其他 agent 运行时区别最大的部分。

### 1. Turn 编排：agent 维护队列，runtime 执行一次迭代

- `agent.Agent` 是整个系统：它自己的 prompt、模型、工具、审批策略、权限规则，加上它维护的会话和 turn 队列。会话管理和 turn 管理都是 agent 自己的方法，没有单独的 turnmanager 结构。
- 队列里是 SQL 中可以前进的 turn。4 个 worker 从队列取 turn，为它建一个 `runtime.Runtime`(这个 turn 的执行器),由 `Step` 执行 ReAct 循环的**一次迭代**:处理上次留下的调用(比如刚被批准的)、召回记忆、拼 prompt、脱敏后调模型、还原、流式输出、判权限、执行工具、把结果写回 SQL。
- 迭代结束时 runtime 报告状态：`Continue` 放回队尾，`Wait` 等用户或子代理，`Done` 结束。循环就是这样由队列一圈圈推进的，runtime 自己不维持循环，也不在两次迭代之间保留任何状态。
- 暂停和恢复不是特殊逻辑：等待中的 turn 不在队列里，也不占 worker；批准、回答、子代理报告一到，它就回到队列。重启后，库里可前进的 turn 就是队列。
- 调度只有一个入口 `settle(threadID)`：它读 SQL 和这个会话在编排器里的调度状态（没有槽位就是空闲或等待；有槽位就是"排队中"或"运行中"），决定入队、派生还没开始的子代理、补记已经结束的子代理报告，或者在会话空闲时把最早排队的 turn 转成进行中。它是幂等的，开始 turn、追加、批准、回答、子代理报告、一次迭代结束、重启都只调用它；编排器每分钟还对所有进行中的会话调用一次（`reconcile`），任何一条路径漏掉的状态转换都会在一分钟内被补上，所以错误分支只需要记日志。
- 调度没有锁。改变调度状态的事件和执行迭代分成两条队列：**事件**（开始、追加、中断、批准、回答、一次迭代结束、子代理报告、每分钟的对账）由一个事件循环 goroutine 逐个处理，它独占全部调度状态（槽位和待运行队列），所以 `settle` 等状态转换天然串行；**迭代**由 4 个 worker 并行执行，跑完把结果作为事件发回循环。`StartTurn`、`SteerTurn`、`InterruptTurn`、`ResolveApproval`、`AnswerUserInput` 把请求发给循环并等它的回答。循环里只有数据库读写和事件推送，不做模型调用和工具执行；数据库本身是单连接，串行处理不损失吞吐。
- 会话忙时的新消息排队成一个新 turn。一个会话同时最多一个进行中的 turn(对话是线性的,每个 item 属于一个 turn),但可以有任意个 `queued` 状态的 turn。`StartTurn` 在会话空闲时直接开始,忙时(有进行中的 turn,或前面还有排队的)只建一个 `queued` turn,输入先存在 turn 行上(`Input`、`CronJobID`),等它真正开始时才写成 user message item:item 按会话连续编号,排队时就写会插进当前 turn 后续的工具调用中间。当前 turn 结束(完成、失败、中断)后,`settle` 按顺序把下一个排队的 turn 转成进行中。中断只结束当前 turn,排在后面的照常开始。想把话插进正在跑的 turn,用 `SteerTurn`。
- 运行中用户发来的消息(`SteerTurn`)直接写进 SQL，下一次迭代就会读到。如果这次迭代已经把请求发出去、模型据此给出了最终回答，编排器发现这个 turn 里有比这次迭代开始时的快照更新的用户消息，就再跑一次迭代而不是结束 turn；这个判断完全来自数据（`ThreadView.InputAfter`），不靠内存标记。
- `Agent` 是一个普通工具，它的结果是"由另一个 agent 回答"(`tools.Await`)。agent 为每个这样的调用开一个子代理会话并让它的 turn 入队；子代理的最终回答写成那条调用的结果，所有回答齐了父 turn 回到队列。子代理不继承父对话，prompt 里要带全它需要的东西。

### 2. 权限系统

- 工具和命令规则：deny / ask / allow，可以带匹配串做细粒度控制；命令按程序名和参数结构化匹配，`sh -c` 脚本和 `env`、`xargs` 这类包装命令会拆开逐条检查。
- 审批策略（never / on-request 等）之上，支持会话级授权：批准过一次，同一命令不再问。
- 跑出沙箱算提权，需要单独批准，allow 规则也不能替代这次批准；审批策略可以完全禁止提权。
- 审批前先冻结要执行的目录、命令、环境变量，批准后按冻结的内容执行。

### 3. 敏感数据处理

- 所有进模型的内容在模型边界统一脱敏：手机号、身份证、银行卡、邮箱、密钥等识别后替换成占位符。
- 占位符在同一进程内确定，同一值始终是同一占位符；turn 暂停恢复后仍能还原。占位符用进程启动时随机生成的密钥做 HMAC，外部无法根据已知值伪造。
- 只有声明需要真实值的工具（如拨号、写回 Excel）在执行前把占位符还原，其余工具只见占位符。

### 4. 执行过程全链路记录

- 运行时没有隐藏状态：模型文本、推理、每次工具调用、审批、计划、提问与回答、工具进度，都先落成一条 thread item 再执行。
- 每条响应记录 token 用量和上下文统计，整个会话可以完整审计(按原样回放还没有实现,见 `docs/deepening-directions.md`)。
- 这份原始轨迹既喂给记忆构建，也是以后做 eval 的原始语料。

### 5. 生成式记忆

- SQLite 存储。抽取是一个和 agent 并列的后台协程（`memory.Run`），由应用在 `main` 里启动，独立轮询已结束的 turn，不通过工具调用；agent 只用召回。
- 用 LLM 从已完成 turn 的轨迹里增量提炼记忆，和已有记忆合并（创建 / 更新 / 废弃），不重复；进行中的 turn 完全不等，避免打断 agent 正在做的事。
- 按工作区或全局召回，用户可编辑、可遗忘，不带版本号；用户手改过的条目受保护，不会被自动覆盖。

## 其他能力

- 内置工具由 agent 在 `Start` 时自己注册,应用不用管:`update_plan`(`tools/plan`,计划与进度)、`request_user_input`(`tools/userinput`,结构化提问)、`Agent`(`tools/subagent`,派生子任务),以及给了 `SkillsDir` 时的技能工具(`agent/skills`)。它们和应用的工具一样受 `Agent.Tools` 允许列表和权限规则约束。
- 工具注册表：应用实现 `tools.Provider` 接入自己的工具，工具的额外能力（权限匹配、本地执行、是否串行）声明在它的 `tools.Spec` 上；注册表负责参数校验、分发和注解（只读、破坏性、访问外部）。
- 定时任务不是 agent 的一部分,而是和 agent 并列的组件,由应用在 `main` 里一起装配:`jobs := cron.New(db, location)` 既是工具提供者(放进 `Agent.Providers`,模型用它创建、暂停、删除、列出任务),又是定时器(`go jobs.Run(ctx, send)`)。定时器只读 cron_jobs 表,到点就调用 `send`,而 `send` 就是用户用的那个入口:`agent.StartTurn(threadID, agent.Input{Text: prompt, CronJobID: id})`。所以定时任务的效果和用户在对话框里发了一条消息一样:会话空闲就开一个普通 turn,忙就排队(见上);消息带着 `CronJobID`,界面能看出是定时发出的。同一个任务在这个会话里已经有一个排队的 turn 时,这次运行只记下、不再重复排队。定时器每分钟检查一次;应用关着时错过的运行在启动后补一次。已知问题:消息发出后、记下"已运行"前进程退出,重启后会再发一次。
- 工具既能由 agent 在 loop 里调用，也能由用户通过接口直接调用（gateway 的 `tool/call`）：比如界面上列出、暂停、删除定时任务，调的就是同一组工具。直接调用的调用者是用户本人，所以不走审批和权限规则；需要 turn 执行环境的工具（`run_command`）和要等外部回答的工具不能直接调用。

## 用法

Ollupi 是 Go 库，不是可执行程序：

```sh
go get github.com/0xfakeSpike/ollupi
```

应用自己负责装配：读配置、打开数据库，填好 `agent.Agent` 后调用 `Start`，再按需要挂上网关和界面。要填的是:名字、prompt(可空,用默认范本)、可选模型 `Models`(第一个是默认)和每个连接的模型客户端 `Connections`、审批策略与权限规则(这两个是默认值,工作区可以覆盖);应用自己的工具 `Providers`(比如 shell、MCP);技能目录 `SkillsDir`、长期记忆 `Memory`,都可选;数据库 `DB`;以及 `OnEvent`。上下文预算不用填:每个模型能用多少输入 token 由 `pkg/llm` 给出(`OpenAI.Limits`),先看 `llm.NewOpenAI` 收到的该连接上的声明,再看内置的常见模型表,两处都没有的模型 `Start` 会拒绝。工具注册表由 agent 自己建。和 agent 并列的后台协程由应用在 `main` 里和 agent 一起启动:记忆抽取 `go mem.Run(ctx)`、定时任务 `go jobs.Run(ctx, send)`(见上)、网关 `server.Serve(ctx, listener)`;agent 不启动、也不等待它们。`Wait` 用于关闭:取消传给 `Start` 的 ctx 之后调用它,它等事件循环和 worker 退出后返回;并列的协程由应用自己等。haoagent 的 `appserver/wire.go` 是一份完整的装配示例，可以照着改。

## 包结构

一个 agent 由核心 loop 和它调用的下游能力组成。目录就按这个组织:

```
ollupi/
  agent/                    agent 本身:定义、编排(事件循环与 worker、排队的 turn)、工作区、审批与提问、子代理、重启恢复、事件
    runtime/                ReAct 循环的一次迭代:调用处理、请求模型、压缩、流式输出
    permission/             授权:权限规则、审批策略、"本会话都允许",判断一次调用 执行/问/拒绝
    systemprompt/           拼 system message
    contextmanager/         拼一次请求、按预算截断和压缩
    skills/                 Agent Skills:技能目录、激活、读取资源
    memory/                 长期记忆:抽取、召回、编辑
  gateway/                  对外协议:WebSocket 上的 JSON-RPC 2.0
  repo/                     数据库(gorm)与会话状态机:每张表的行结构体、读写方法和不变量
  tools/                    工具契约与注册表(最底层之一,repo 也依赖它)
    shell/  mcp/            具体工具:命令、远程 MCP(应用按需装配)
    plan/  userinput/       update_plan、request_user_input(agent 内置)
    subagent/               Agent 工具:派生子任务(agent 内置)
    cron/                   定时任务:工具、任务表和定时器(和 agent 并列,应用装配)
  pkg/                      通用组件,不知道 agent 的存在
    llm/  sandbox/  jsonrpc/  redact/
```

依赖只能往下指:

```
gateway
    ↓
agent(系统:编排、工作区)
    ↓
runtime(一次迭代)
    ↓
systemprompt  contextmanager  permission  memory  skills  tools/*(具体工具)
    ↓
repo(数据库与会话状态机)
    ↓
tools(工具契约)    pkg/*
```

agent 是编排核心,runtime 是执行核心,中间那一层是它们共用的组件。真正使用这些组件的是 runtime:agent 在每次迭代前把它们构建或取出(这个 turn 所用模型的上下文管理器、按 turn 快照的权限检查器、技能目录、记忆),填进 `runtime.Runtime` 交给它;agent 自己只直接用到 permission(校验工作区设置)和 systemprompt(默认 prompt)。组件不知道 agent 和 runtime 的存在。

**依赖都是真实的,不靠 interface 绕开。** 所有和数据库打交道的代码都在 `repo`:一张表一个文件(`workspaces.go`、`threads.go`、`turns.go`、`items.go`、`cron_jobs.go`、`memories.go`、`memory_events.go`、`memory_state.go`),里面是这张表的行结构体(gorm 据此建表)、它的读写方法和它的状态规则;`db.go` 是打开数据库和公用的小工具。行结构体就是业务直接用的类型,没有另一份业务模型,也没有转换函数:会话、turn、item 就是 `repo.ThreadRow`、`repo.TurnRow`、`repo.ItemRow`(带 JSON 标签,也就是接口返回给界面的样子),它们的状态机也只在 repo 里:判断(进行中的 turn、等待中的请求、子任务调用、会话状态)和转换(`BeginTurn`、`RecordItem`、`EndTurn`、`EndSubagentTurn`、`RecoverItems`、`AddUsage`)放在同一个包,一个会话同时只有一个进行中的 turn、只写进行中 turn 的 item、item 不改类型和所属 turn、已准备的调用不能改,这些不变量都在这些转换的事务里检查,带条件的 upsert 再兜底一次。runtime、contextmanager、permission、gateway 都要读这些状态,它们都在 agent 之下,所以状态机只能放在它们都能 import 的 repo,而不是 agent。嵌套的字段用 gorm 的 JSON 序列化各存一列(turn 的模型,item 的审批、调用结果、冻结的命令等),用量是内嵌的三列。工作区、定时任务、长期记忆也一样:记忆就是 `repo.MemoryRow`(证据来源用 JSON 序列化存成一列),待处理的增量就是 `repo.MemoryEventRow`(追加式队列,不合并、不带版本号),记忆状态就是 `repo.MemoryStatus`(几个计数加上 `memory_state` 那一行,上次的报告也是 JSON 序列化的一列)。记忆的业务规则(哪些变化算证据、遗忘过的证据不再生成、删除的源要废弃相关记忆)留在 `agent/memory` 包里。事务也不另设类型:`DB.Tx(func(tx *repo.DB) error)` 把同一个 `*repo.DB` 绑在事务上交给回调,所以每个读写方法在事务里外都能用,不会有"事务里一份、事务外一份"的重复方法。别的包需要的数据结构也尽量直接用现成的:contextmanager 拼的就是 `[]llm.ChatMessage`,技能目录工具直接返回 `skills.Skill`,定时任务工具直接用 `repo.CronJobRow`。不需要为了"分层"而定义 `Persistence`、`Store` 这类只有一个实现的接口。同理,runtime 直接持有 `*tools.Registry`、`*skills.Catalog`、`*memory.Manager` 和要执行的 `repo.TurnRow`,gateway 直接持有 `*agent.Agent`。runtime 写 item 用的是 agent 交给它的函数(写库并通知订阅者),所以它碰不到会话的存储。

整个库只有 2 个 interface,都是因为真的有多个实现:

- `tools.Provider` — 工具有很多种(shell、MCP、cron,还有应用自己的工具)。一个工具能做什么,写在它的 `tools.Spec` 上(权限匹配 `PermissionContent`、本地执行 `Local`、串行 `Serial`),不靠类型断言去猜它实现了哪些可选接口。
- `jsonrpc.Handler` — 通用的 JSON-RPC 连接回调 gateway。

设计上的三条规则:

1. **只能往下 import,同层之间互不依赖。** 所以 memory 不知道 cron,shell 不知道 mcp。工具契约 `tools` 是最底层之一,所以放在顶层而不在 `agent/` 下:它不 import 任何内部包,repo 的 item 行直接存它的类型(`Prepared`、`Await`、`Annotations`、`ContextArtifact`)。所以 **`tools.Prepared` 的结构就是数据库里的存储格式**:给它加减字段会改变已存的冻结记录,而"本会话都允许"按整个 `Prepared` 相等来匹配,改了结构,旧的会话授权就不再生效。
2. **runtime 只通过 `tools` 契约使用工具**,不直接 import 任何具体工具。加一个工具或去掉一个工具都不用改 runtime。
3. **不随手加 interface。** 只有真的有多个实现时才用 interface。

项目不带测试,代码里不写注释(只保留 `//go:build`、`//go:embed` 这类编译指令),设计说明都在这份 README 里。

**agent 与 runtime 的几个关键行为**:

- **命令在哪里跑是 shell 自己的事。** agent 只知道会话属于哪个项目,把项目根目录放进 `tools.Caller.Root` 交给工具(system message 里也会写明项目根目录)。shell 每次准备命令时,用它和自己的配置现算命令能在哪些目录跑:有项目就在项目根目录下,没有就在配置的命令目录下。命令的冻结记录(`tools.Prepared`)绑定了目录和配置摘要(`SettingsDigest`);批准之后、执行之前如果设置或项目目录变了,这条命令不执行,模型收到"请重新调用"的结果,在同一个 turn 里重新发起,按新设置准备,需要的话重新审批。设置因此立即生效,不用开新 turn。
- **不读用户目录里的说明文件。** 不加载任何 `AGENTS.md` / `CLAUDE.md`。agent 自己的 prompt 由应用通过 `Agent.Prompt` 给出;留空时用 `systemprompt` 里写好的默认范本(以 `Agent.Name` 自称的个人助理:先弄清需求、能做就做、多步任务列计划、不越出项目目录、难以撤销的改动先说明、做完自查、照顾隐私)。用户的偏好由长期记忆自动学到,不需要用户自己写规则。
- **脱敏是双向的。** 发给模型前,把敏感值(手机号、邮箱、IP、密钥等)替换成稳定的占位符;模型的回答(流式文字、最终文字、工具调用参数)在被记录或交给工具之前,先把占位符还原成原始值。所以敏感值不会发给模型服务商,而会话记录、界面和工具看到的都是真实值。工具调用参数是 JSON,还原时按 JSON 字符串转义(比如私钥里的换行),参数仍是合法 JSON。记忆抽取请求同样先脱敏,抽取出的记忆再还原。
- **只读工具并行执行。** 模型一次可以发起多个工具调用。runtime 按顺序逐个检查(参数、权限规则、审批);相邻的、已放行的只读调用会同时执行,其他调用单独执行,并等前面的调用完成。工具执行时不碰这次迭代的任何状态:进度行通过 channel 发回,结果作为返回值交回,所有记录都只由执行这次迭代的那一个 goroutine 按顺序写入,不依赖锁。驱动单一外部资源的只读工具(比如用户的浏览器)可以在 Spec 上标 `Serial`,不参与并行。
- **runtime 不认识任何具体工具。** 所有工具都来自注册表,runtime 只认工具结果的一种形态:`tools.Result.Await`,表示"这次调用在 turn 内答不了,由用户或另一个 agent 回答"。runtime 把它记在 item 上(调用保持进行中),继续处理同一回复里后面的调用;处理完后只要还有等待中的调用,这次迭代就以 `Wait` 结束。回答由 agent 写成那条调用的普通结果,turn 回到队列。审批不一样:等批准的调用会挡住后面的调用,因为后面的调用可能依赖它的效果。
- **模型跟着 turn 走。** 模型是一个值 `llm.Model{Name, Connection, Thinking}`,turn 开始时确定并存进 turn,之后这个 turn 的每次请求、重启后的恢复都用它。 用户可选的模型就是 `Agent.Models`,一组 `llm.Model`,每个连接一个,连接名(`Connection`)就是它的标识;不另设"模型档案"类型。`model/list` 返回这组模型和默认连接,`turn/start` 用 `connection` 指定这一轮用哪个,不指定就沿用这个会话上一轮的,新会话用 `Models` 的第一个。 模型(`llm.Model`,存进 turn 的值)和怎么连上它(`*llm.OpenAI`,客户端)是两回事:`Agent.Connections` 按连接名给出每个连接的客户端,`Start` 时检查 `Models` 里每个连接都有客户端;执行一个 turn 时,agent 按这个 turn 的 `Model.Connection` 取出客户端交给执行器。
- **没有子代理这个实体。** 派生出来的 turn 就是同一个 agent 在干活:同样的 prompt、工具、模型(沿用父 turn 的)和审批策略,任务内容来自父 turn 那次 `Agent` 调用的参数;每个派生的 turn 放在一个子会话里(有 `ParentThreadID`,会话列表里不显示),上下文只有这个任务。它不能再派生:system message 里写明了这一点,工具列表里没有 `Agent`(加了一条 deny 规则),就算真发起了 `Agent` 调用,agent 派生时也会拒绝。父 turn 等待时不在队列里;子 turn 结束时报告写回那次调用,全部到齐父 turn 才回到队列。
- **记忆在 turn 开始时冻结一次。** 第一次模型调用前,runtime 把本 turn 的用户消息和 contextmanager 给的预算交给 `memory.Context`,把返回的条目和文本记成 `memoryContext` item;之后的请求、审批后的恢复、重启都只读这个 item。召回只发生在这个 turn 还没有任何模型调用、也没有 `memoryContext` item 的那次迭代:召回为空或失败,整个 turn 就不带记忆;运行中追加的消息(`SteerTurn`)不会触发重新召回。这样一个 turn 内 prompt 的前缀不变,便于服务商的前缀缓存。"用哪些消息做查询、挑哪些、怎么控制长度、读失败怎么办"全在 memory 包里;runtime 只负责时机和记录。
- **上限只防失控。** 每个 turn 默认最多 200 次模型调用、400 次工具调用、连续 10 次失败(`agent/runtime` 里的常量,不对外配置)。Codex 没有上限,这些数值只是为了拦住陷入死循环的模型,不会打断正常的长任务。
- **不另记执行日志。** 执行过程的每一步都已经是 SQL 里的 item(模型调用的上下文统计和用量、工具调用的参数、审批、结果和错误、等待、压缩、强制收尾的原因),所以 runtime 和工具注册表不再往日志里重复写一遍;日志只记录 SQL 里没有的东西,也就是写库本身失败、后台循环出错、连接异常这类错误。
- **暂时没有缓存。** 所有数据只存在数据库里,每次读都查库。代码在做决定的地方读最新状态,不把读到的会话一路往下传:中间可能有别处的写入(runtime 在 worker 里写 item、子代理刚报告完),往下传的就是旧数据。读的成本留给以后的缓存:缓存放在 `repo`(所有写入都经过它,写时同步更新),事务内的读取绕过缓存。
- **工具输出截断时保留头尾。** 输出超出预算时,保留开头三分之一和结尾三分之二,中间注明省略了多少字符;报错通常在结尾。截断发生在脱敏之前,所以切点会避开会被脱敏的片段(`redact.Spans`):一个密钥要么完整保留再被替换成占位符,要么整个省略,不会留下半截认不出来的明文。

**各包职责**:

- `agent` — 整个系统:它自己的 prompt、模型、工具、审批策略、权限规则(审批策略、权限规则和模型在 turn 创建时存进 turn;派生出来的子会话额外禁止再派生),以及一条跨线程共享的 turn 队列和多个常驻消费者。文件按概念分:`loop.go` 是事件循环本身(调度状态、`post`/`call`、派发给 worker)和 worker 侧(`worker`、`run`,只有这两个函数不在循环里跑);`schedule.go` 是调度决策(`settle`、每分钟的 `reconcile`、一次迭代结束后继续/等待/结束的判定、停止、重启恢复);`turns.go` 是对外的 turn 接口(开始、追加、中断、批准、回答)和它们在循环里的实现、结束 turn;`subagents.go` 是子代理的派生、报告和对账;`requests.go` 是只读的等待请求查询。队列元素只带 threadID 和 turnID,消费者按 ID 从 SQL 现查 thread、workspace 和 items,再用 turn 里记录的模型连接取 LLM 客户端;线程、工作区和业务数据之间没有别的传递通道,也没有内存缓存。会话、turn、item 的状态转换调用 repo(见上),agent 不存任何状态,每次读都查库;每次写入之后由 agent 推送事件、推进调度。工作区的业务也在 agent(`workspaces.go`):`AddWorkspace` 把路径规范成真实目录(`sandbox.CanonicalDirectory`,shell 判断命令目录用的也是这一个),同一个目录再加一次复用已有的工作区,已归档的顺便恢复;`RenameWorkspace`、`ArchiveWorkspace`、`UpdateWorkspacePermissions`;带审批策略和权限规则时先对照工具注册表校验;归档了的工作区不能开新会话、新 turn。所以不用 gateway、直接嵌入 agent 的应用拿到的是同一套工作区规则;项目根目录交给工具(`tools.Caller.Root`),目录能不能跑命令由 shell 在跑命令时判断。审批策略和权限规则按 会话覆盖 > 工作区设置 > agent 默认 解析(`policyFor` / `rulesFor`)。消费者把 turn 交给 runtime 跑一次迭代,按结果放回队列、等待或结束;用户的新消息(`StartTurn`,忙时排队)、追加消息(`SteerTurn`)、批准、回答、中断、子代理的派生和报告、重启恢复都是它的方法。agent 有要告诉用户的事(流式文字、新的或完成的 item、等批准或回答、turn 开始和结束、会话状态),就调用应用给的 `OnEvent` 回调;它由事件循环或 worker 调用(流式文字和迭代中新记的 item 来自 worker),所以要能并发调用、必须很快返回,而且不能回调 agent 的方法:循环正在等它返回,回调会让循环等自己。
- `agent/runtime` — ReAct 循环的一次迭代(`Runtime.Step`):先处理上次留下的调用,再请求一次模型并处理它的调用,返回 `Continue` / `Wait` / `Done`。`Runtime` 是一个 turn 的执行器,不是全局共享的对象:worker 每跑一次迭代就由 agent 新建一个,填上它用的部件(这个 turn 所在连接的模型客户端 `LLM`、工具注册表 `Registry`、技能目录、上下文管理、记忆)和这次要执行的东西:turn 本身(`Turn` 就是 `repo.TurnRow`,模型也从它上面取)、prompt、这个 turn 可用的工具(`Tools`)、权限检查器、调用者(会话、项目根目录)、写 item 的函数,以及这个 turn 已有的 item(`Items`)和之前各 turn 的 item(`History`,只用来拼上下文和查本会话的授权)。这次迭代里新产生的 item(模型的回答、工具调用和结果、进度、压缩)都由它记下并追加到 `Items`;迭代中的工作状态(system message、脱敏表、用量)是它的不导出字段,迭代结束就和它一起丢掉,迭代之间不保留状态。并行执行的工具只拿到注册表和调用者,碰不到执行器的状态。文件按内容分:`runtime.go`(Runtime 与一次迭代的流程)、`calls.go`(把每条调用判定成 run / 并行读 / 跳过 / 等待 / 停止的动作,不执行)、`execute.go`(按顺序执行动作,相邻只读并行、串行单独跑、停止后跳过)、`chat.go`(和模型的一次对话:拼请求、需要时先压缩、脱敏后发送再还原、检查回答、记下它请求的工具调用)、`stream.go`(流式文字)。每个响应的 token 统计挂在它的 agentMessage item 上;只有工具调用的响应挂在第一条 toolCall 上,contextmanager 用它校准预算。
- `agent/permission` — 授权判断:权限规则(deny / ask / allow,Claude Code 语法)、审批策略(`untrusted` / `on-request` / `never`)、用户之前"本会话都允许"的回答。`New` 在启动时对照工具目录检查规则,`Decide` 对一次调用按固定顺序给出 执行 / 问用户 / 拒绝:越权提权(`never` 下)→ deny → ask → allow → 本会话已允许 → 提权要问 → 审批策略;allow 规则放行的提权命令仍要问。工具只声明事实(只读、访问外部、规则匹配的内容 `tools.Content`、命令怎么跑),判断都在这里。规则匹配的内容是结构化的:`Match` 是程序名(去掉路径)加参数,不含环境变量;`Parts` 是它实际会运行的各条简单命令(`sh -c` 脚本按 `;`、`&&`、`|` 等拆开,`env`、`nohup`、`timeout`、`xargs`、`sudo` 等包装命令剥掉外层);`Opaque` 表示内容没法完全看清(带环境变量、脚本里有 `$(...)`、反引号、`eval` 等)。deny 和 ask 只要 `Match` 或任何一条 `Parts` 命中就生效;带匹配串的 allow 只在不 `Opaque` 时生效,而且有 `Parts` 时每一条都要被 allow 规则覆盖;不带匹配串的 allow(只写工具名)放行这个工具的一切调用。它只管决策,配置存在 `repo.WorkspaceRow`(审批策略和规则都是工作区字段)和 turn 的快照里;规则只有一种类型,`permission.Rules` 就是 `repo.PermissionRules` 的别名,存储和判断之间不做转换。
- `agent/contextmanager` — 把 system message 和对话拼成一次请求、按预算截断和压缩。预算是当前 turn 所用模型的输入上限(来自 `pkg/llm`);请求大小先按字符估算,再用同一模型上一次请求的真实 prompt tokens 校准,刚压缩完还没有新参照时按之前请求的"真实值 ÷ 估算值"放大(不低于 1)。同一次模型回复的工具结果共用一个预算池(输入上限的 25%,按调用数平分,单条最多 6000、至少 512 token);整个请求放不下时把每条结果的上限逐次减半(最低 256)直到放得下,所以请求不会超过模型上限。只有压缩真能腾出空间时(上次压缩后的对话至少占输入上限的 10%)才触发压缩;压缩请求本身也先截断结果,实在放不下才丢掉最早的整段对话。给出记忆、技能目录和已激活技能能占的预算(`MemoryBudget`、`SkillBudget`、`ArtifactBudget`)。
- `agent/systemprompt` — system message 的全部组成都在这里,按顺序拼:agent 自己的 prompt(`Agent.Prompt`,留空用默认范本 `AgentPrompt`)、子代理说明(子代理才有)、循环规则、执行环境、技能目录(在预算内整条列出,放不下的说明省略了几个)。每个 turn 开始时拼一次,turn 内不变,便于前缀缓存。
- `tools` — 工具契约(`Provider`、`Spec`、`Result`、注解、本地命令的冻结记录 `Prepared`)与注册表;调用时通过 context 告诉工具进度回调和所属工作区。
- `tools/shell` — `run_command` 工具:命令能在哪些目录跑(由调用者的项目根目录和配置现算,`dirs.go`)、命令参数、提权申请(`require_escalated`)、超时与黑名单(按 `command.go` 拆出的每条简单命令检查;它只是辅助,安全边界是沙箱和权限规则),命令按模型给的 argv 原样执行,不做改写;环境变量名必须是合法标识符;命令经 `pkg/sandbox` 执行;`shell.Config` 是用户可配的 exec 设置(带 JSON 字段名,`Normalize` 是唯一一份校验规则),agent 自己的数据目录和 skills 由它固定保护,应用不必也不能关掉。
- `tools/mcp` — 远程 MCP 服务器的客户端,把它的工具接入注册表。
- `tools/plan`、`tools/userinput` — `update_plan`(计划就是调用参数)和 `request_user_input`(结果是 `Await` 给用户,回答成为调用结果)。
- `tools/cron` — 定时任务的四个工具(创建、暂停/恢复、删除、列出),`*cron.Jobs` 本身就是工具提供者,读写 `repo` 的 cron_jobs 表:每个任务属于一个会话(`thread_id`,会话删除时一并删除),存着下次运行时间(`next_run_at`)。`Run(ctx, send)` 是定时器:每分钟查一次到期任务(`DB.DueCronJobs`),调用 `send` 发出,再算下次运行时间。它不 import `agent`,只认识传进来的 `send` 函数。
- `tools/subagent` — `Agent` 工具:模型用它派生子任务(简短说明 + 完整任务),结果是 `Await` 给另一个 turn;派生由 agent 完成。
- `agent/skills` — Agent Skills:`skills.New(dir)` 扫描、校验技能目录,得到的 `*skills.Catalog` 既是 runtime 把技能列进 prompt 的来源,本身也是提供列出、激活、读取资源三个工具的提供者。
- `agent/memory` — 长期记忆:后台抽取(只处理已完成/失败/中断的 turn 和工作区、会话、定时任务的变化)、列出(`List`)、用户编辑与遗忘(`Edit`、`Forget`,文字长度和凭据只在这里检查一次)、状态(`Status`);以及每个 turn 开始时给它的记忆(`Context`):在调用方给的 token 预算内挑选、排序并渲染成 `memoryContext` item。记忆事件由 SQLite 触发器写进事件队列,触发器的 SQL 也在这里(`triggers.go`),`memory.New` 时用 `DB.ReplaceTriggers` 重建;它只关注终态 turn 和少数字段,不追每次 item 写入。没有配置记忆的应用不会新装触发器。处理失败的事件按指数退避重试,第 10 次失败后停下(留在队列里,状态里计为失败,`lastError` 说明原因),不再无限重试。后台抽取的循环(`Run`)由应用和 agent 并列启动;agent 只通过 runtime 用召回(`Context`)。

- `gateway` — WebSocket 上的 JSON-RPC 2.0 网关,方法名对齐 Codex app-server;另有 Ollupi 自己能力的方法(`memory/*`、`workspace/*`,以及直接调用工具的 `tool/call`)。应用自己的方法(比如读写它的配置文件)通过 `Options.Methods` 注册,不属于 Ollupi 的协议,也不能覆盖协议里的方法名。 应用用 `gateway.NewServer(agent, options)` 建好服务(长期记忆的接口直接用 `agent.Memory`),在 `agent.Start` 之前把 `agent.OnEvent` 接到 `server.Deliver`,再 `server.Serve(ctx, listener)`。`Deliver` 只是挑出关心这个会话的连接,把通知放进各自的发送队列,马上返回。`tool/call` 在单独的 goroutine 里执行,不挡住这个连接的其他请求,连接断开时取消;`turn/interrupt` 带上 turnId,在编排器的锁内核对,不会误中断刚开始的下一个 turn(`Agent.InterruptTurn(threadID, turnID)`)。 gateway 只做协议:解码参数、调用 agent 的方法、编码结果和通知,不含业务规则,也不直接碰数据库;`workspace/*` 方法对应 agent 的工作区方法(`workspaces.go`),改动后向所有连接广播 `workspace/changed`。
- `repo` — 数据库和数据模型,通过 gorm:打开 SQLite(WAL、外键、单连接),用行结构体建表(gorm 的 AutoMigrate,开发期没有 schema 版本,格式不兼容时重建数据库,不做旧库兼容迁移);gorm 不建模的触发器由使用它的包提供 SQL,通过 `ReplaceTriggers(前缀, SQL)` 按名字前缀整组重建(目前只有记忆)。每张表的读写方法,以及会话状态机的判断和转换(见上)。不变的列由带条件的 upsert 保护:改了不该改的列,写入就失败。
- `pkg/llm` — 和模型对话的类型(`Model`、`ChatRequest`、`ChatResponse`)与 OpenAI 兼容 Chat Completions 客户端 `*llm.OpenAI`。模型客户端不是 interface:所有模型都走 OpenAI 兼容接口,需要假模型时把客户端指向一个本地的假服务器即可。
- `pkg/sandbox` — macOS Seatbelt 沙箱:策略、profile、域名代理、进程监管、主机进程运行。几条安全边界:
  - **受保护名在所有可写位置都不能创建**(工作区、额外可写目录、临时目录;`node_modules`、`vendor`、`Pods` 例外),`.git` 的 hooks 和 config 也一样,目录整体克隆(`clonefile`)被禁止。Seatbelt 分不清"搬进来"和普通的建目录、删目录,只有这样才能堵住"先在临时目录建好 `.git/hooks` 再整体 mv 进工作区"。代价是沙箱里的 `git init`、`git clone` 在临时目录也会失败。已知缺口:`node_modules` 里的受保护名仍能被移到工作区别处(git hooks 和 config 除外)。
  - **缓存默认只读**,只有使用时会按哈希校验内容的缓存可写(目前只有 npm 的 `_cacache`)。Go 模块和构建缓存、cargo、pip、uv、pnpm、Yarn 等缓存在使用时不校验内容,可写就能污染用户下一次在沙箱外的构建。代价是沙箱里拉取没缓存过的依赖会失败,需要提权或在沙箱外先拉好。
  - **凭据文件不可读**:`DefaultDenyRead` 覆盖 SSH、云厂商、包管理器(`.npmrc`、`.yarnrc*`、Maven、Gradle、Composer、NuGet)、数据库(`.pgpass`、`.my.cnf`)、gh/glab 等 CLI 的令牌和浏览器数据。
  - **网络代理**只放行允许的域名;解析到回环、私网(RFC1918、IPv6 ULA)、100.64/10、链路本地或组播地址的连接会被拦下,除非在允许列表里写明了这个域名或地址。
  - **命令结束时清理所有后代进程**:每次运行带一个随机的环境变量标记(`<EnvMarker>_RUN_ID`),结束、超时或取消时先杀进程组,再扫描本用户的进程,把带同一标记的进程和它们的子进程、进程组一起停掉再杀掉。用 `setsid` 脱离进程组的后台进程也逃不掉,所以 `nohup … &`、tmux 服务这类进程不会活过命令本身。清掉自己环境变量又脱离进程组的进程仍能逃过,非 macOS 上只杀进程组。
- `pkg/jsonrpc` — WebSocket 上的双向 JSON-RPC 2.0 连接。每个连接有自己的发送队列和写 goroutine:发出的响应、通知、请求先入队,按顺序写出;一个读得慢的客户端只会拖慢它自己;积压超过 16 MiB 就断开它。处理函数可以返回 `jsonrpc.Async`,在单独的 goroutine 里执行并在完成时回复,连接关闭时它的 ctx 被取消,`Serve` 等它们结束才返回。
- `pkg/redact` — 敏感值脱敏与还原:`Mask`、`Restore`、按 JSON 转义还原的 `RestoreJSON`、流式还原 `Stream`,以及找出会被脱敏片段的 `Spans`。

## 与 Codex / Claude Code 的能力对照

完整的逐项能力对照见 [docs/agent-capabilities.md](docs/agent-capabilities.md)。
