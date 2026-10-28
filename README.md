# Ollupi

Ollupi 是用 Go 写的 agent 运行时库。它把模型、工具、权限、沙箱、脱敏和记忆组织成一条执行链路：每一步都先写进数据库再执行，所以整条链路可以审计，进程崩溃后也能接着跑。应用 import 它，接上自己的工具、技能和界面，就能做出自己的 agent 软件。

桌面端界面见 [ollupi-desktop](https://github.com/0xfakeSpike/ollupi-desktop)。

## 特色

**1. 所有对话共用一个队列和一组执行槽位**
所有会话里能往前走的 turn，包括子代理的，都排在同一个队列里，由一组固定的执行槽位消费（默认 32 个）。turn 一旦要等审批、回答或子代理，就让出槽位，下一个能跑的 turn 马上补上，所以槽位不会被等待中的任务白白占着。一个任务派出几十个子代理时，它们和其他会话一起并行，能并行的工作会尽快一起完成。槽位满时，普通会话排在子代理前面，同类按开始时间先后；先派出的子代理先完成，它们的父任务也就先完成。队列就是库里可前进的 turn，不在内存里，重启后原样还在。

**2. 状态全在库里，崩溃后能接着跑**
模型的文字、每次工具调用、审批、提问和回答都先落成一条 item 再执行。runtime 每次调模型前都从库里重读状态，内存里没有隐藏状态。工具调用分 `pending`（确定还没执行）和 `inProgress`（可能已经生效）两种状态，所以重启后能准确判断哪些可以重做、哪些要先核对。重启后的恢复和平时的调度走同一个函数 `reconcile`，没有另一条恢复路径。

**3. 调度不用锁，并发按模型限流**
所有改变调度状态的事件由一个事件循环逐个处理，运行 turn 的 goroutine 停下时把结果作为事件发回，所以调度状态不需要锁。并发上限加在模型调用上（每个连接默认 4 个），不加在工具执行上：跑十分钟测试的工具不会占住别的会话调模型的名额。会话忙时新消息自动排队，运行中插话（`SteerTurn`）从下一次模型调用起生效。

**4. 结构化的权限系统**
deny / ask / allow 规则分三层叠加（agent 默认、工作区、会话），下层只能加规则，去不掉上层的 deny。命令按程序名和参数结构化匹配：`sh -c` 脚本和 `env`、`xargs`、`sudo` 这类包装命令会拆开逐条检查，看不清的命令一律问用户。审批前先冻结目录、命令和环境变量，批准后按冻结的内容执行。跑出沙箱算提权，必须单独批准。

**5. macOS Seatbelt 沙箱**
命令默认在沙箱里跑：凭据文件不可读，缓存默认只读，`.git` 的 hooks 和 config 写不进去。网络经过代理，只放行允许的域名，并拦下解析到内网的地址。命令结束时，它派生的所有后台进程都会被清掉。

**6. 模型边界上的双向脱敏**
发给模型前，把手机号、身份证、银行卡、邮箱、密钥等替换成占位符，模型的回答在记录和交给工具之前再还原。所以敏感值不会发给模型服务商，而会话记录、界面和工具看到的都是真实值。占位符用进程启动时生成的随机密钥做 HMAC，外部无法伪造。用户可以随时关掉或打开脱敏。

**7. 生成式长期记忆**
后台协程按游标读取已结束的 turn，用模型提炼出记忆，再和已有记忆合并（新建、更新、废弃）。记忆按内容分三个范围：全局、general、工作区。thread 开始时载入一份快照，保持请求前缀不变，有利于服务商的前缀缓存。另有只读的 `memory_search` 工具。用户改过或遗忘的条目不会被自动覆盖。

**8. 类型化的协议契约**
网关是 WebSocket 上的 JSON-RPC 2.0，方法名对齐 Codex app-server。`gateway/protocol.go` 里的一张表既用来分发请求，也用来生成 `protocol.schema.json`。内置工具的参数类型同时生成模型看到的 schema 和契约里的 schema，两边不会对不上。测试会用假模型跑一段完整会话，按契约校验收发的每一条消息。

**9. 依赖少而直接**
依赖只往下指：gateway → agent → runtime → 各组件 → repo → tools / pkg，同层之间互不 import。行结构体就是业务类型，没有转换层。整个库只有真正存在多个实现的几个 interface（如 `tools.Provider`）。

此外还内置子代理（`Agent` 工具）、计划（`update_plan`）、结构化提问（`request_user_input`）、Agent Skills、MCP 客户端（stdio 和 streamable HTTP）、定时任务，以及超长工具输出的截断和读回。

## 包结构

```
agent/            编排：事件循环、调度、turn、工作区、子代理、恢复
  runtime/        ReAct 循环：跑到必须等待或给出最终回答为止
  permission/     规则、审批策略和"本会话都允许"
  contextmanager/ 按预算拼请求、截断、压缩
  memory/         记忆抽取、快照、搜索、编辑
  skills/         Agent Skills
  systemprompt/   拼 system message
gateway/          JSON-RPC 网关和协议契约
repo/             SQLite（gorm）、表结构和会话状态机
tools/            工具契约和注册表；shell、mcp、cron、plan、userinput、subagent
pkg/              llm、sandbox、redact、jsonrpc，都不依赖 agent
```

## 用法

```sh
go get github.com/0xfakeSpike/ollupi
```

由应用自己装配：打开 `repo.DB`，用 `agent.New(agent.Config{...})` 创建 agent（填模型 `Models` 和它们的 `Connections`、权限默认值 `Settings`、工具提供方 `Providers`、可选的 `SkillsDir` 和 `Memory`），再调用 `Start(ctx)`。和 agent 并列的组件由应用一起启动：记忆抽取 `go mem.Run(ctx)`、定时任务 `go jobs.Run(ctx, send)`，以及用 `gateway.NewServer` 建好的网关 `server.Serve(ctx, listener)`。关闭时先取消 ctx，再调用 `agent.Wait()`。完整的装配示例见 ollupi-desktop 的 `sidecar/internal/host/host.go`。

修改协议后用 `go generate ./gateway` 重新生成 `protocol.schema.json`。

## 许可

[AGPL-3.0](LICENSE)
