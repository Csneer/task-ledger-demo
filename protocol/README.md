# 采用 task-core-draft-1

本项目基于 [Agent↔Task Bridge](https://github.com/Csneer/agent-task-bridge) 的实验任务协议，实现独立的 Go authority，以真实应用验证其可用性。当前已锁定上游规格、建立本地 S1 binding 契约，并实现 Go 状态机、SQLite 事务、HTTP 后台与轻量 Skill。核心证据见 [S2 实施结果](../docs/validation/s2-results.md)，实际客户端验收与未覆盖边界见 [S3 验收记录](../docs/validation/s3-agent-acceptance.md)；本项目尚未声明完整 binding 符合性。

| 输入 | 固定值 |
|---|---|
| 协议版本 | `task-core-draft-1` |
| 上游提交 | [`e6dae9a6127537af94f0f5127e8d590a1e6a3f1a`](https://github.com/Csneer/agent-task-bridge/commit/e6dae9a6127537af94f0f5127e8d590a1e6a3f1a) |
| 规范语义 | [固定提交的 semantics.md](https://github.com/Csneer/agent-task-bridge/blob/e6dae9a6127537af94f0f5127e8d590a1e6a3f1a/protocol/core-draft/semantics.md) |
| 本地结构契约 | [schema.json](upstream/schema.json)，逐字节复制 |
| 来源和完整性 | [upstream.json](upstream.json)、[SHA256SUMS](upstream/SHA256SUMS)、[上游许可证](upstream/LICENSE) |

语义规范与 Schema 共同构成协议；只验证 JSON 结构不能证明状态行为符合。语义文档使用固定提交链接，本仓库没有离线全文镜像。不会跟随上游 `main` 自动更新，也不会使用 sibling checkout 或本机绝对路径作为依赖。

## 核心与应用的映射

| 日常动作 | 核心操作或事实 | 本项目宿主/业务补充 |
|---|---|---|
| 人或 Agent 发布任务 | `task.create`，必须有 goal/expected_result | 项目关联、可选定向领取策略与创建一起提交 |
| 修改工作预期 | `task.revise` | 发起方编辑，展示历史与变化原因 |
| 查看可领任务、指定 Agent | 任务当前状态；核心无发现方法 | 稳定身份目录、查询与 ClaimPolicy；指定不等于领取 |
| 主动承接、开始执行 | `task.claim`、`run.start` | 宿主判断准入，核心判断单承接和依据版本 |
| 汇报执行与证据 | `run.report` | 时间线展示、摘要和证据引用 |
| 正式交付 | `run.submit` | 结果与产物展示；仍等待发起者决定 |
| 接受或拒绝结果 | `task.decide` | 验收入口解析真实 issuer；不由管理员代替 |
| 无法继续、退出、返工、取消 | `run.fail`、`assignment.release`、`task.reopen`、`task.cancel` | 明确触发者和理由；不自动复活旧承接 |
| 多 Agent 分工 | 多个有关系的 Task | 分工、选人、依赖与汇总规则 |
| 项目接续 | Task 快照和保留的执行事实 | 固定来源版本的 Handoff、读取游标及公共查询 |

参与者引用采用 `{kind: human|agent, id}`。Task 使用 `{authority_id, task_id}`；数据库主键、URL 和查询不能只拿 task_id 代替完整归属。消息的 `HostContext` 是受信宿主输入，不是网络客户端可以自报的身份。

## 本地绑定与上游演进

本项目提供独立 Go HTTP 服务，客户端按公开 binding 接入，无需导入服务内部包。已发布 M1 的 HTTP 服务、Bearer/租约逻辑及客户端不兼容本草案；本项目不会透传到 M1 后声称采用了新核心。

HTTP、身份、查询、cursor 和应用策略在[本项目 S1 绑定基线](../docs/binding.md)单独定义；它们不是上游已经承诺的能力。未来可把通用需求形成提案，回馈上游，但不能私自修改核心含义。

升级时按[升级记录](UPGRADES.md)记录新提交、字段/语义变化、持久数据与客户端影响、回归证据；更换本地文件和摘要必须是显式升级。模型对照、独立客户端互操作与压力验证的证据范围见[验证计划](../docs/validation.md)。
