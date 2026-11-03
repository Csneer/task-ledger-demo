# Agent 快速接入与主子任务闭环

Task Ledger 保存协作事实；你当前的助手会话负责拆解、启动执行者和检查实际成果。当前 Skill 是 Python 标准库 HTTP 助手与协作说明，支持持久请求意图，但不是后台调度器。下面可以用于 Codex、Claude Code、Pi 等能读取文件和运行 Python 的宿主；各宿主原生子代理的完整适配仍需逐项验收。

## 1. 准备服务与身份

需要已有可写实例和项目。管理员按[后台接入](backend.md#身份登记与凭据更新)登记稳定 ActorRef，为主 Agent 和各独立执行者提供各自的凭据与客户端配置，再重启加载凭据。普通执行者只需 read/write，不应使用人的 owner/admin 凭据。当前没有一键注册发证。

公开的 [task.pokison.cn](https://task.pokison.cn/) 提供演示浏览和游客文字投稿，不是 Agent 执行写入的地址；维护者使用自己的私有 API 或 SSH 转发入口。通过 SSH 时，客户端连接转发后的 `http://127.0.0.1:<本地端口>`；直连远端使用正常校验的 HTTPS。

管理员交给某个 Agent 的私有目录应类似如下，目录 `0700`、文件 `0600`，由实际运行该 Agent 的操作系统用户持有：

```text
/private/path/worker-b/
  client.json
  bearer
  intents/
  sessions/
```

`client.json` 示例；相对路径以该配置文件所在目录为基准：

```json
{"service_url":"http://127.0.0.1:8090","credential_file":"bearer","intent_dir":"intents"}
```

`bearer` 的内容由管理员带外提供，不写入任务、提示词或仓库。HTTPS 自有 CA 可配置 `ca_file`。完整 `skills/task-ledger/` 目录可从源码或解包后的发行包获得；让助手显式读取其中的 `SKILL.md` 即可使用，也可安装到宿主支持的 skills 目录。无需服务端 SDK 或额外 pip 依赖。

## 2. 先验证连接

将以下路径替换为实际值，在一个**全新**的私有检查目录执行。`mkdir` 遇到已有目录会停止，避免覆盖历史请求或回执。此处只查询，不创建任务：

```sh
set -eu
ledger_skill=/absolute/path/task-ledger
ledger_config=/private/path/worker-b/client.json
ledger_check=/private/path/worker-b/connection-check
umask 077
mkdir "$ledger_check"
cat > "$ledger_check/info.json" <<'JSON'
{"binding_version":"ledger-http-draft-1","operation":"service.info","body":{}}
JSON
cat > "$ledger_check/workspace.json" <<'JSON'
{"binding_version":"ledger-http-draft-1","operation":"workspace.snapshot","body":{}}
JSON
python3 "$ledger_skill/scripts/ledger.py" query --config "$ledger_config" \
  --request "$ledger_check/info.json" > "$ledger_check/info-response.json"
python3 "$ledger_skill/scripts/ledger.py" query --config "$ledger_config" \
  --request "$ledger_check/workspace.json" > "$ledger_check/workspace-response.json"
```

核对响应中的 authority、协议版本、项目和目标 TaskRef。`HTTP 200` 只说明查询成功，不代表已经领取；可信身份与能力可用相同凭据查询 `GET /v1/identity`，不是向 JSON 自报 Actor。具体封套与逐步字段见 [HTTP 参考](../skills/task-ledger/references/http.md)。

## 3. 把已有父任务交给主 Agent

父任务由需要最终验收的人或上游 Agent 发起。将占位值替换后，把以下模板交给主 Agent：

```text
读取 /absolute/path/task-ledger/SKILL.md，以及 references/collaboration.md。
私有客户端配置：/private/path/main-agent/client.json。
父任务：{authority_id: "my-authority", task_id: "parent-task"}。
项目：my-project；工作区：/absolute/path/repository。
可用执行者及配置：worker-b → /private/path/worker-b/client.json，
worker-c → /private/path/worker-c/client.json（均已预先登记）。

先查任务与当前执行归属；未承接时登记自己的 session，再 claim/start。
在已授权范围内拆出可以独立交付、独立验收的子任务。
由你创建子任务，记录 parent 关系、目标 Agent、文件归属、基线与验收条件。
创建确认后，使用当前宿主允许的派生工具交接 TaskRef、配置位置及工作范围。
不要替子 Agent 领取，不要给它你的 session proof。
等待实际 Submission，读取产物并复验；不以聊天回复或进程退出代替交付。
你作为子任务 issuer 接受或拒绝。需要重做时明确 reopen，保留旧交付。
完成集成验证后，在父任务提交汇总及子 TaskRef，等待父任务原发起者验收。
保留各步原请求和回执；未知结果用原 ID、原请求核对，不重复新建。
```

任务的预期、文件 ownership、基线和交付要求应放进任务正文或命名空间扩展，不能只留在主会话。凭据和机器私有配置路径仅通过受控的本地交接提供，不写入公开任务。没有可用执行者身份或派生工具时，如实报告缺口，不伪造领取或启动成功。

## 4. 子 Agent 收到的交接

```text
读取 /absolute/path/task-ledger/SKILL.md。
使用你自己的配置 /private/path/worker-b/client.json。
领取任务 {authority_id: "my-authority", task_id: "child-task-b"}。
工作区：/absolute/path/repository；仅负责任务约定的文件和成果。
你不是唯一贡献者，保留其他参与者的修改。

查询真实任务、身份与基线；为本次执行建立自己的 session/proof 文件。
合法 claim → start 后执行工作，使用 run.report 留下进展和验证摘要。
实际完成后 run.submit，包含成果、证据、限制和可访问产物，再结束自己的 session。
提交后核对任务为 submitted，等待 issuer 决定；不要自行验收子任务或父任务。
不借用主 Agent 的 session，不把同一身份当作可接管其他进程的证明。
```

子任务由主 Agent M 创建时，`issuer=M`。在 `task.create.body.relations` 中记录 `{kind:"parent",target:{authority_id,task_id}}`；在 `application` 中指定项目和 `{claim_policy:{mode:"target",target:{kind:"agent",id}}}`。完整合法封套见 [HTTP 参考](../skills/task-ledger/references/http.md)；这些关系和策略不负责启动进程。

每个并行执行使用独立 session；一个 session 同时最多绑定一个当前 Assignment。宿主原生子代理 ID 不会自动变成服务内 ActorRef。若多个进程使用同一稳定 Actor，仍须分开 session/proof；这只能区分执行归属，不能提供独立身份的审计或互相隔离。需要明确区分各执行者时，由管理员预先登记不同 Actor 并分配各自凭据。

## 5. 完整验收责任

| 阶段 | 发出操作的主体 | 必须核对的事实 |
|---|---|---|
| 发起父任务 P | 人或上游发起者 | 目标、预期结果、最终验收者明确 |
| 承接 P | 主 Agent M | 自己的 session、Assignment 和 Run |
| 创建 C1/C2 | M | 每项独立 TaskRef、issuer=M、parent 指向 P |
| 执行并提交 C1/C2 | 各执行 Agent | 自己的 session，claim/start/report/submit 的实际回执 |
| 验收子交付 | M | 当前 Submission、实际产物和复验结果；通过后 task.decide |
| 汇总并提交 P | M | 集成结果、子 TaskRef、验证与未完成项，父任务进入 submitted |
| 验收 P | P 的原发起者 | 核对父 Submission 后作决定，接受后才 completed |

关系不授予验收权，也不自动汇总或推进父状态。如果子任务由其他主体创建，主 Agent 不能仅凭父子关系验收它。所有子任务完成后仍需要主 Agent 的集成验证与父交付。

## 6. 回写与中断接续

| 情况 | 当前正确处理方式 |
|---|---|
| 实施进展 | `run.report` 记录任务进展；`session.update` 只记录会话活动，不能替代报告或交付 |
| 产物完成 | `run.submit` 写正式交付；issuer 的 `task.decide` 才记录验收 |
| 超时、断线、5xx | 保留 intent 和原 ID/原字节，核对或重试原请求；历史回执不代表当前仍可执行 |
| 上下文压缩或新会话接续 | 从私有检查点读取配置路径、TaskRef、session 文件位置、原请求/回执和未完成项，再查 `task.snapshot`、`session.get` 与当前绑定；检查点目前需会话自行维护，Skill 不自动生成长期 handoff |
| 原会话证明仍在 | 核对当前绑定和任务状态后继续；不要因旧回执成功就再次执行外部工作 |
| 同 Actor 换 session | 合法 `session.transfer` 要有源、目标双方证明，并满足同项目等约束；单凭 session ID 不能恢复 |
| 证明丢失或换 Actor | 由合法主体 release 当前承接，再由符合当前领取策略的新 session/Actor 重新 claim/start；定向任务更换 Actor 时，issuer 须在 offered 状态通过 `claim_policy.update` 明确调整目标。不同 Actor 不能使用 transfer，旧执行记录保留 |
| 执行失败或交付被拒绝 | issuer 明确 reopen 后进入新承接/Run；不覆盖旧报告或 Submission |
| 进程退出、心跳消失 | 不自动完成、失败或释放任务；先核对事实与外部工作状态，避免重复副作用 |

需要继续保存的检查点只引用秘密文件位置，不复制 Bearer/proof 到正文。`session.end` 不完成任务也不释放承接。服务可以阻止失效证明的新写入，不能证明旧进程的文件修改或外部操作已经停止。精确规则见[会话与恢复契约](contracts/identity-session.md)和[协作约定](../skills/task-ledger/references/collaboration.md)。

## 7. 已验证与下一步

2026-10-31 的[四客户端验收](validation/s3-agent-acceptance.md)跑通了一个父任务和三个子任务：本机 Codex 发布，Pi、Claude Code、远端 Codex 独立执行提交，主 Codex 验收子交付并汇总，父任务的测试 human issuer 最终接受。父子状态没有自动级联。

该轮子任务及预期结果预先定义，外层 Python 脚本负责启动客户端；Codex 关闭 `multi_agent`，Claude 仅开放文件和 Shell 工具。Pi 使用 Skill，其他客户端依据公开 HTTP 契约调用。因此它未证明自主拆解、原生递归派生、复杂代码集成或上下文耗尽后自动恢复。2026-11-01 的[前端验收](validation/frontend-results.md)另有本机 Codex 使用 Skill 完成一个真实代码审查任务的证据。

下一项有价值的验收是：主 Agent 自行拆解一项有明确标准的真实代码任务，使用宿主原生工具派生至少两个执行者，全部通过 Skill 留下独立事实；保留一次拒绝重做和一次主会话中断接续，最终由主 Agent 完成集成并提交父任务。当前文档提供操作路径，这项端到端验收仍待完成。后台自动 launcher、自动调度和长期 handoff 也尚未实现。
