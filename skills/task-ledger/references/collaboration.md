# 代码协作与交付

用于已授权的项目实施分工。服务保存任务事实；当前会话负责选择和派生子 Agent、交接工作区、核实实际产物。服务不执行任务正文，也不自动启动进程。普通单任务领取沿用 [HTTP 消息与恢复](http.md)，无需为了登记实施拆出更多任务。

## 先确定工作区与责任

默认按下表选择一次，写入任务后由双方核对；不要在执行中静默换分支或目录。

| 工作方式 | 适用情况 | 接收方实施前核对 | 交付方式 |
|---|---|---|---|
| 共享 checkout | 同一次集成内、文件归属互不重叠的并行子任务 | 同一仓库、当前分支、基线 HEAD、已有未提交修改；明确自己可写的文件和禁止修改的共享文件 | 留下限定范围的工作区修改，报告文件和验证；由集成者统一核对 |
| 独立 worktree | 同一文件的不同方案、需要独立提交历史、长时间独立分支工作 | 主仓库身份、精确 base commit、自己的分支与 worktree 路径、依赖是否已进入基线 | 提供分支/提交或有明确范围的 diff；集成者按已有授权合入 |

共享 checkout 的不同 Agent 不能分别 checkout、reset、stash 或清理整个仓库来“恢复自己的环境”；已有修改属于其他参与者，任务未授权就不覆盖。发现两个任务需要同一个文件时，先报告集成者，由一个明确负责人修改共享接口；另一个任务继续做不依赖冲突文件的工作。任务没有独占仓库，只拥有说明中的文件或模块。

独立 worktree 只能隔离文件修改，不隔离数据库、监听端口、依赖服务或浏览器会话。测试时使用本任务明确的运行目录和端口，交付时报告自己启动的进程与保留资源。不要复制其他 Agent 的 session proof 来接管其执行。

基线最少记录仓库身份、分支、HEAD 和 dirty 状态。接收方用 `git status --short`、`git branch --show-current`、`git rev-parse HEAD` 核对；环境要求 RTK 时按环境约定加前缀。共享 checkout 的 HEAD 未变化不代表其他 Agent 没有新改动，开始编辑和交付前都核对相关文件。独立 worktree 不自行 rebase 到新的 base，除非集成者已明确更新交付基线。

## 发布 → 当前会话派生 → 主动领取

1. 发起者在已有授权范围内创建一个可独立说明预期结果、可独立验收的 Task。核心 `goal/expected_result` 说明要完成什么；文件 ownership、基线、交付与验证要求放在 `constraints/context` 或点分命名空间的 `extensions`。不要依赖只有父 Agent 能看到的私有聊天。
2. 子任务在 `relations` 中使用 `{kind:"parent",target:{authority_id,task_id}}` 指向父任务。并行方案使用不同 TaskRef，可用 `related` 关联原任务；不把多份并行执行放在同一 Task 的当前承接里。
3. 需指定接收 Agent 时，`task.create.application.claim_policy` 使用现有 `{mode:"target",target:{kind:"agent",id}}`；只有已登记、enabled 的 Agent 可作 target。让人领取时用当前支持的 workspace 策略，不伪造 agent 身份。定向任务仍是 `offered`，不会替接收者建立 Assignment。
4. 发起者读取成功回执并查询当前 Task 后，再通过当前会话已获准使用的子 Agent 能力传达 TaskRef、服务/skill 的配置位置、工作区位置和范围。只交接私有凭据文件的位置与访问方式，不把 Bearer 或 session secret 写进任务、提示词或产物。此流程不调用 `wake.request`，不配置后台 launcher。
5. 接收方读取公开 Task 与本地仓库规则，核对身份、工作区和权限；创建自己独立的 ActivitySession，再 `claim → start`。两进程使用同一个稳定 Agent 身份时也必须使用不同 session 文件；服务中的当前绑定决定谁能写 Run，不以“属于同一个 Codex”推断已持有执行权。
6. 子 Agent 在自己的 Task 中报告实施与证据、提交实际结果；父会话用公开快照核对交付和文件变化。子 Agent 的聊天汇报或进程退出不能代替 `run.submit`，也不能代替 issuer 的 `task.decide`。

若接收方缺少合法身份配置，报告缺口并停止领取；不让父 Agent 伪装成接收者 claim。若 Task 已被合法领取，则查询当前事实并报告，不另建一个相同任务掩盖冲突。子 Agent 能否继续拆分取决于用户授权、当前会话工具与资源限制；不能因为协议允许 `task.create` 就自行扩大实施范围。

以下只是应用扩展片段，不是新增核心字段，也不是可执行权限。填写本次真实值；不要把秘密或机器私有路径写入公开字段：

```json
{
  "io.taskledger.collaboration": {
    "workspace_mode": "shared_checkout",
    "repository": "task-ledger-demo",
    "owned_paths": ["web/graph.js", "web/graph.css"],
    "read_only_paths": ["web/app.js"],
    "delivery": "working_tree_diff",
    "commit_policy": "follow_existing_user_authorization"
  }
}
```

基线分支和真实 commit 在发布时补入同一扩展或任务上下文；示例没有提供能替代现场核对的基线。扩展只记录约定，不能使服务强制文件所有权、改变 issuer 或授予 Git 权限。

## 重做与并行方案

- `failed/rejected` 任务由 issuer 明确 `task.reopen`，恢复 `offered`；接收者重新 claim/start，得到新的 Assignment、generation 和 Run，旧报告与交付保留。
- `completed/cancelled` 为终态，不直接重开。需要再次尝试时，新建关联任务并说明前次结果、差异目标和验收预期。
- 多开方案由多个独立 Task 交付。可以在命名空间扩展记录共同组 ID 和方案序号，页面据此展示扇出关系；这些标记不自动产生派发、汇总或验收规则。
- 多任务发布不是一笔事务。每个 create 使用独立稳定 ID；部分成功或结果未知时保存已确认 TaskRef 和原请求，继续核对原意图，不整批重新生成 ID。

## 提交与集成验收

每份交付至少写清：实现了什么、修改文件、实际验证命令和结果、未完成项、需要集成者处理的冲突，以及代码所在工作区/分支/已存在的提交。证据引用应让验收方能够实际读取；仅给本机私有路径不代表对方可访问。验证失败不能包装成“基本完成”，没有运行的检查写明未验证。

默认不因任务交付而自动 commit、push、建 PR 或合并。已有用户授权 commit 时，只提交自己拥有且已核对的文件，报告 commit ID；共享 checkout 禁止把其他 Agent 的修改一并提交。已有 push 或 PR 授权仍需遵守目标分支与仓库约定。未授权提交时，工作区 diff 和可复验结果就是合法交付材料。

集成者先核对 TaskRef、最新 revision、Submission 和实际文件，再按涉及的路径运行组合验证。通过后由该子任务 immutable issuer 接受；需要调整则拒绝并给出具体理由，或要求报告澄清。父任务仍保持自己的状态，所有子任务完成不触发自动验收。父 Agent 汇总产物并提交父任务，父任务 issuer 才能作最终决定。

不把“收到子 Agent 最后一条消息”“模型退出码为零”“分支已有提交”或“画布亮绿”单独当作集成成功。停止条件是本任务实际交付已提交、责任范围与验证证据清楚，且需验收时明确处于待验收；是否完成以服务记录的 issuer 决定为准。
