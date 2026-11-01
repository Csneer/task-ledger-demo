---
name: task-ledger
description: 通过 Task Ledger 的公开 HTTP 协议登记实施会话、发布或领取任务、汇报交付与验收，并保留可恢复的写请求；用于已配置服务的人与 Agent 协作、子任务分工和代码交付，不提供后台自动唤起。
---

# Task Ledger

使用 `ledger-http-draft-1` / `task-core-draft-1`，让人与 Agent 的任务、执行和验收留下可接续事实。后端权限来自已配置凭据，不自报 Actor/Host，也不直读服务数据库。

## 接入

此目录可整体复制到客户端支持的 skills 目录；也可直接读取本文件调用脚本，无需服务端 SDK。需要 Python 3 标准库。配置与状态保存在工作仓库之外的私有目录，目录 `0700`，配置、Bearer 文件、session 文件均为 `0600`。管理员先登记稳定身份，再带外配置凭据；显示名和模型资料不能授予权限。

配置示例（路径相对配置文件；不把真实凭据放进文档或参数）：

```json
{"service_url":"http://127.0.0.1:8090","credential_file":"bearer","intent_dir":"intents"}
```

HTTPS 可加 `ca_file` 指定受信 CA；HTTP 仅接受显式 loopback IP。Bearer 文件存 32 随机字节的无填充 base64url 文本，由服务管理者提供。脚本不跟随重定向，不自动使用系统代理，不忽略 TLS 校验。

```sh
python3 <skill-dir>/scripts/ledger.py query --config <config.json> --request <query.json>
python3 <skill-dir>/scripts/ledger.py action --config <config.json> --request <action.json>
python3 <skill-dir>/scripts/ledger.py command --config <config.json> --request <command.json> --session-file <session.json>
```

请求文件须为完整 JSON 封套，字段与最小链路见 [HTTP 消息与恢复](references/http.md)。脚本发送原始 UTF-8 字节，不把精确数字转为浮点数。stdout 为响应，stderr 为 HTTP 状态；退出码 `0` 表示 HTTP 200，`2` 表示其他 HTTP 状态，`1` 表示本地或连接错误。将各步响应保存在私有目录中（例如先设 `umask 077`，再将 stdout 重定向到独立文件），以核对组合步骤的中间结果；不能只保存最终一步。

## 工作链路

1. 查询 `service.info` 和 `workspace.snapshot`，核对 authority、项目、身份与当前任务。发现不兼容协议或没有合法凭据时说明缺口；不要换身份绕过。
2. 用 `session-secret --session-id <稳定ID> --output <session.json>` 先持久保存本会话证明。输出只有 `session_id/proof_digest`，再以独立 request ID 调用 `session.begin`。重复运行同一生成命令会读取已有证明，不轮换。会话来源只记录公开摘要。
3. 已有任务先读当前事实，再 `task.claim`；自发任务先用自己的身份 `task.create`，随后领取。指定 Agent 只限制领取资格，不意味着已经承接。每个写步骤使用不同、稳定的 ID。
4. 从当前 Task 读取 assignment、generation、expectation version，调用 `run.start`。后续 `run.report/submit/fail` 使用当前 session 证明和 Run ID；每步重读或采用刚返回的 Task revision，避免凭旧快照继续写。
5. 提交结果后由不可变 issuer 读取实际交付并 `task.decide`。Agent 自发任务可以如实自验收，但不能称为人的批准；需要人验收的任务由人发起。其他管理员没有代验收权。
6. 明确 `session.end` 并记录实施摘要；它不会完成 Task 或释放承接。跨 Agent 接续需合法 release 后重新 claim/start；同身份换会话需显式 `session.transfer` 和双方证明，不使用别人的 session ID 猜测接管。

## 代码分工与子任务

需要委派子 Agent、多人共用仓库、独立 worktree、并行尝试或集成代码时，按需读取 [协作与交付约定](references/collaboration.md)。先把任务目标、文件归属、工作区方式、基线和验收责任写清，再发布任务；接收方通过自己的实施会话主动领取。默认使用当前会话可用的子 Agent 能力，不请求后台 launcher，不将发布、唤起或子 Agent 已启动视为领取成功。

共用 checkout 时不同任务只改各自明确拥有的文件；独立试验或需要独立提交历史时使用单独 worktree。工作区方式不改变单 Task 单当前承接者的协议规则。发布任务不授权 commit、push 或合并；这些动作沿用用户和仓库已有授权。

## 结果未知与恢复

写操作在发送前将原请求完整落入私有 intent 目录，fsync 后原子发布。相同服务、配置路径、请求类型和 ID 再次调用时要求请求字节完全相同；即使服务器允许某些数字写法等价，本助手也不替换已保存的原文。不要删掉意图文件来强行复用 ID。

超时、连接中断、5xx 或意外响应都不能证明没提交。保留原封套和 ID，明确重试同一步以取得历史回执，再查询当前事实。脚本不自动重试，不把 create→claim→start 当一笔事务，也不会为了恢复创建替代 Task。回执是历史，不授予当前执行权。

session 证明只从独立私有文件形成请求头；`--target-session-file` 用于 transfer 的目标证明。秘密不放入正文、产物、报告或提示词。begin/rotate 的 digest 属于请求内容，原请求会保存在私有意图记录中，公开查询不会返回它。

发生冲突、身份/会话拒绝或输出不符合预期时，先核对服务公开事实再决定修正，不盲目重领或重做外部副作用。后台唤起、自动调度和 handoff 不由此 Skill 实现；使用当前会话的子 Agent 能力时仍按公开协议领取、交付与验收。
