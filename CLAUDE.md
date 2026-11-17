# CLAUDE.md

## 文档

引擎的长篇文档在 [ollupi-docs](https://github.com/0xfakeSpike/ollupi-docs)（`docs/engine/`），描述的版本记在它的 `docs/.vitepress/engine.json`。改动下面这些时，在提交说明里写清楚行为变了什么，好让文档跟上：

- 权限判定和审批策略：`policy`，以及 `run` 里对每个调用的判定
- 沙箱规则和 `Box` 接口：`pkg/sandbox`
- 协议：`gateway/protocol.go`（会改变 `protocol.schema.json` 的 sha256）
- `host.Config`（`host`）的字段和 `schedule.New`（`schedule`）的参数，以及它们的默认值
- `bench` 的实验和数字：`bench/README.md` 里的结论要能对应到原始输出
