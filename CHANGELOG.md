# Changelog

产品版本、binding 和上游协议分别管理。当前候选尚未创建 Git tag 或 GitHub release。

## Unreleased — Task Ledger Demo v0.1.0 Developer Preview

- Go `taskledgerd` 后台：SQLite/WAL、Bearer 凭据映射、11 个核心操作、10 个应用动作、7 个公开查询和签名 cursor。
- 可安装 Python Skill：实施会话、领取、报告、提交、验收和未知写恢复。
- Task Orbit 同源前端：任务链总览、单任务历史回放、子任务、重做、并行尝试、窄屏布局和真实事件顺序展示。
- 发布候选已形成：干净源码发行包、安装 smoke、持久用户服务、停机备份/迁移说明和隔离恢复演练；[工程验证记录](docs/validation/release-prep-20261101.md) 已补齐，真人验收与公开发布仍待完成。

当前限制：采用固定的 `task-core-draft-1` 和 `ledger-http-draft-1`，不是 Agent↔Task Bridge v0.1.0 的 `bridge-m1-1` daemon；面向本机或受控私网单 authority。未提供公网多租户、自动唤起 Agent、handoff、跨 authority 互操作或生产容量承诺。
