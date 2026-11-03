# Changelog

产品版本、binding 和上游协议分别管理。

## Unreleased

- 便捷 Agent Skill：一次 `setup` 后直接 `take / report / submit / decide / resume`，自动管理协议字段、会话、执行绑定和私有恢复检查点，保留原始 JSON 客户端。
- 未知写入阻止新业务写入，按原操作 ID/原字节显式重放；身份漂移、部分领取、并发 profile 和截断响应均有针对性验证。
- `make check` 新增真实 Go 服务上的便捷 CLI 主子任务、拒绝重做与重启接续验证；接入和验证范围见 [Agent 指南](docs/agent-quickstart.md)与[验证记录](docs/validation/easy-skill-results.md)。

## [v0.1.0](https://github.com/Csneer/task-ledger-demo/releases/tag/v0.1.0) — 2026-11-01 · Developer Preview

- Go `taskledgerd` 后台：SQLite/WAL、Bearer 凭据映射、11 个核心操作、10 个应用动作、7 个公开查询和签名 cursor。
- 可安装 Python Skill：实施会话、领取、报告、提交、验收和未知写恢复。
- Task Orbit 同源前端：任务链总览、单任务历史回放、子任务、重做、并行尝试、窄屏布局和真实事件顺序展示。
- 发布 Linux amd64/arm64 发行包、校验清单、初始化工具与持久用户服务模板；tag 检查通过后发布同一次 CI 构建的附件。arm64 仅交叉编译。
- 完成安装 smoke、停机备份/迁移说明和隔离恢复演练；[工程验证记录](docs/validation/release-prep-20261101.md) 已补齐。维护者已授权发布开发者预览，真人日常使用验收仍待完成。

当前限制：采用固定的 `task-core-draft-1` 和 `ledger-http-draft-1`，不是 Agent↔Task Bridge v0.1.0 的 `bridge-m1-1` daemon；面向本机或受控私网单 authority。未提供公网多租户、自动唤起 Agent、handoff、跨 authority 互操作或生产容量承诺。
