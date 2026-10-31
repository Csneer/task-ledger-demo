# 消息与恢复参考

所有接口为 POST，Content-Type 为 `application/json`，使用已配置 Bearer。脚本从配置与 session 文件添加请求头；JSON 不包含身份授权。完整契约见 [Task Ledger binding](https://github.com/Csneer/task-ledger-demo/blob/main/docs/binding.md)，以当前服务声明的版本为准。

## 封套

查询文件示例：

```json
{"binding_version":"ledger-http-draft-1","operation":"workspace.snapshot","body":{}}
```

动作文件示例（先运行 `session-secret`，用它返回的 digest 替换示例标记）：

```json
{"binding_version":"ledger-http-draft-1","request_id":"begin-session-unique-id","operation":"session.begin","expected_revision":0,"body":{"session_id":"session-unique-id","project_id":"project-id","goal":"公开的实施目标","proof_digest":"REPLACE_WITH_GENERATED_SHA256"}}
```

核心命令文件示例：

```json
{"binding_version":"ledger-http-draft-1","command":{"protocol_version":"task-core-draft-1","command_id":"claim-unique-id","task":{"authority_id":"authority-id","task_id":"task-id"},"expected_revision":1,"operation":"task.claim","body":{}},"application":{"session_id":"session-unique-id"}}
```

这里的 ID、revision、digest 均需替换成这次操作的真实值；任务已有 revision 未必是 1。可使用 UUID 作为新命令 ID，但重试时不生成新 ID。请求文件和 intent 目录不要提交到 Git。

## 逐步字段

| 操作 | 核心 `body` 或动作 `body` | `application` 与版本 |
|---|---|---|
| `task.create` | `goal`, `expected_result`；约束/资源/关系为可选字段 | `{project_id,claim_policy:{mode:"workspace"}}`；Task revision=0 |
| 定向 create | 同上 | policy=`{mode:"target",target:{kind:"agent",id:"登记的ID"}}`，不是自动 claim |
| `task.claim` | `{}` | `{session_id}`，header 来自同一 session 文件 |
| `run.start` | `assignment_id`, `generation`, `basis_version` | `{}`；basis=当前 expectation_version |
| `run.report` | `assignment_id`, `generation`, `run_id`, `message` | `{}`；不等同 ActivitySession heartbeat |
| `run.submit` | `assignment_id`, `generation`, `run_id`, `basis_version`, `summary`；可选 artifacts/extensions | `{}`；basis 固定为 Run 开始时的版本，不能替换为后来修订版本 |
| `task.decide` | `submission_id`, `outcome:"accepted"或"rejected"`, `reason` | `{}`；调用者必须为 issuer；不要求原执行 session |
| `assignment.release` | `assignment_id`, `generation`, `reason` | `{}`；合法 assignee 或 issuer 明确释放 |
| `run.fail` | `assignment_id`, `generation`, `run_id`, `reason` | `{}`；失败保留记录，issuer 可显式 reopen |
| `task.reopen` / `task.cancel` | `reason` | `{}`；只能在规则允许状态，由 issuer 发起 |
| `session.update` | `session_id`, `phase`, `summary` | 动作封套；expected_revision 使用 Session revision |
| `session.end` | `session_id`, `outcome:"done"或"failed"或"cancelled"`, `summary` | 动作封套；Session revision，结束会话不替代任务交付/验收 |

每次核心命令的 expected_revision 都是 Task revision。`CommandSuccess.result.task` 返回更新后的 Task；Run ID 在 `current_run_id`，Assignment ID 在 `current_assignment_id`，承接代次是 `generation`。submit 后当前指针会清空，待验收对象在 `pending_submission_id`。历史 Run 及其依据保留在 records。

查询 `task.snapshot` 的 body 为 `{task:{authority_id,task_id}}`；读取 `result.bundle.snapshot.task` 与 `result.bundle.snapshot.records`。`session.get` 的 body 为 `{session_id}`，返回 session 与 current_binding。查询不需要 session proof，但需要当前 read 凭据。

## 事件接续

`workspace.snapshot` / `task.snapshot` 返回一致快照、水位与 `event_cursor`。`events.read` 请求 body=`{cursor,limit:100}`。done=false 时保存 next_cursor；done=true 时保存 follow_cursor。只有处理完整批次后才记录完整批次水位。

按 authority+event_id 去重，按对象 revision 防倒退。cursor 是不透明字符串，不能自行拼接、解码修改或跨 Actor 复用；normal restart 后应能继续。`CURSOR_RESET_REQUIRED` 时重新取得快照；`FORBIDDEN` 时核对当前身份权限，不以新 cursor 绕过。

## 常见拒绝

- `STALE_REVISION`：重读当前 Task 或应用实体，再作新决定；不要只递增本地数字。
- `STALE_ASSIGNMENT`：旧承接已失效，不能继续提交旧执行。
- `SESSION_MISMATCH` / `SESSION_PROOF_REQUIRED`：检查本人 session 文件和当前绑定，不借用其他进程身份。
- `SESSION_ENDED`：会话已结束；需要合法 transfer 或释放后新承接。
- `IDEMPOTENCY_CONFLICT`：同 ID 原请求不同；保留原意图并厘清已执行结果。
- `CAPACITY_LIMIT`：请求或响应超出已声明容量；不清理服务历史回执来解除限制。

共享工作空间的 read 权限可以读取全部公开任务。指定 Agent 不是保密 ACL；不向任务输入私人密钥或完整会话。
