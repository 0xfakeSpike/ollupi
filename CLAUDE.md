# CLAUDE.md

## 文档

引擎的长篇文档在 [ollupi-docs](https://github.com/0xfakeSpike/ollupi-docs)（`docs/engine/`），描述的版本记在它的 `docs/.vitepress/engine.json`。改动下面这些时，在提交说明里写清楚行为变了什么，好让文档跟上：

- 权限判定和审批策略：`agent/permission`
- 沙箱规则和 `Box` 接口：`pkg/sandbox`
- 协议：`gateway/protocol.go`（会改变 `protocol.schema.json` 的 sha256）
- `orchestrator.Config`（`agent/orchestrator`）的字段和默认值
- `bench` 的实验和数字：`bench/README.md` 里的结论要能对应到原始输出
