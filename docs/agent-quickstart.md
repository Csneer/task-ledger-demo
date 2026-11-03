# Agent 快速接入与主子任务闭环

日常接入只需一个 [Task Ledger Skill](../skills/task-ledger/SKILL.md) 和 Python 3。`agent.py` 自动处理协议封套、revision、会话证明、Assignment 和 Run；Agent 专注于任务目标、实际工作和验收理由。它可以供能读取文件和运行 Python 的 Codex、Claude Code、Pi 等宿主使用。

## 一次配置，之后直接工作

先由服务管理者按[后台接入](backend.md#身份登记与凭据更新)准备项目、稳定 Agent 身份和独立 read/write 凭据。将整个 `skills/task-ledger/` 目录复制到宿主的 skills 目录，或直接让助手读取其中的 `SKILL.md`。这一版便捷入口在源码中提供，旧 v0.1.0 发行附件仍是原始 JSON 客户端。

公开 [task.pokison.cn](https://task.pokison.cn/) 用于浏览和游客投稿。Agent 执行使用私有 API；远端可通过 SSH 转发为本机 loopback HTTP，直连时使用正常校验的 HTTPS。

```sh
ledger_skill=/absolute/path/task-ledger
ledger_profile=/private/path/my-agent

python3 "$ledger_skill/scripts/agent.py" --profile "$ledger_profile" setup \
  --url http://127.0.0.1:8090 \
  --credential-file /private/path/my-agent-bearer \
  --project my-project
python3 "$ledger_skill/scripts/agent.py" --profile "$ledger_profile" check
python3 "$ledger_skill/scripts/agent.py" --profile "$ledger_profile" tasks
```

凭据文件由管理者带外交付，权限为 `0600`。`setup` 创建私有 profile，核对服务返回的身份和 authority；后续写入再次核对，凭据轮换不能悄悄切换成另一 Actor。每个并行执行者使用自己的 profile。工具不自助注册身份或发证。

以后只需把下面这段交给 Agent，**不需要给它完整协议 JSON**：

```text
读取 /absolute/path/task-ledger/SKILL.md。
使用 profile /private/path/my-agent，处理任务 parent-task。
工作区 /absolute/path/repository。
先 show 和 take；实施期间 report，完成后 submit 并如实 finish。
需要分工时创建子任务，用当前宿主的原生子代理能力交接给独立身份。
由你检查并验收自己发起的子任务，集成后提交父任务给原发起者验收。
中断后先 resume；未知写入按 operation ID 原样 retry。
```

## 常用命令

以下 `ledger` 为 `python3 <skill-dir>/scripts/agent.py --profile <profile-dir>` 的简写：

| 目的 | 命令 |
|---|---|
| 看可用任务与详情 | `ledger tasks` / `ledger show --task ID` |
| 领取并开始 | `ledger take --task ID` |
| 留下进展 | `ledger report --task ID --message '已实现，测试中'` |
| 交付 | `ledger submit --task ID --summary '成果、验证与限制' --artifact '可访问引用'` |
| 结束自己的会话 | `ledger finish --task ID --outcome done --summary '已交付待验收'` |
| 创建子任务 | `ledger create --parent P --target worker-b --goal '目标' --expected-result '验收条件'` |
| 接受指定交付 | `ledger decide --task C --submission S --outcome accepted --reason '复验通过'` |
| 拒绝并返工 | `ledger decide --task C --submission S --outcome rejected --reason '缺失条件'`，然后 `ledger reopen --task C --reason '按反馈重做'` |
| 核对中断状态 | `ledger resume --task ID` |
| 重放未决写入 | `ledger retry --operation OPERATION_ID` |

`create` 支持重复 `--constraint`，将文件归属、基线和交付条件放进任务，避免只留在聊天里。`submit` 支持多个 `--artifact`，保存引用而不上传文件。JSON 回执会给出新任务、操作、会话和交付 ID；验收应使用实际读取并核对过的 Submission ID。

## 主 Agent 如何派发和收回结果

1. 父任务由需要最终验收的人或上游 Agent 创建。主 Agent 用自己的 profile 领取父任务。
2. 主 Agent 拆分独立成果，用 `create --parent --target` 为每个执行者发布任务。每项任务有自己的目标、验收条件和文件归属。
3. 创建确认后，主 Agent 用当前宿主原生工具启动子代理，交接任务 ID、Skill 路径、接收者自己的 profile 和工作范围。
4. 子代理独立 `take → report → submit`，提交产物和验证证据。主 Agent 用 `show` 查交付，读取真实文件并复验，再 `decide`。
5. 若拒绝，主 Agent `reopen`，原执行者重新 `take`，产生新 generation 和 Run。全部子交付接受后，主 Agent 集成、提交父结果，由父 issuer 最终验收。

可直接给子代理：

```text
读取 /absolute/path/task-ledger/SKILL.md。
使用你自己的 profile /private/path/worker-b，处理任务 child-task-b。
在 /absolute/path/repository 中只修改任务约定的文件。
你不是唯一贡献者，保留其他参与者的修改。
show 后 take，执行并 report；完成后 submit，包含实际验证与产物，再 finish。
不要自行验收任务；等待创建者决定，需要返工时重新核对并领取。
```

凭据和证明内容不进入提示词。宿主子代理 ID 不自动等于服务 ActorRef；独立身份需要事先登记。任务发布只建立事实，不启动模型，也不替别人领取。同一任务只有一个当前承接者，多个执行者使用多个子任务。共享 checkout 与 worktree 的具体约定见[协作参考](../skills/task-ledger/references/collaboration.md)。

## 接续与“回血”的实际范围

`resume` 会核对本地检查点与服务中的 Task、Session、当前执行绑定和未决操作。原 profile 仍在时，新模型会话可以接着使用已有 session，不用手抄 Assignment、generation 或 Run ID。实际工程上下文仍来自工作区、测试、报告和产物，工具不会重建整个聊天记录或自动重启模型。

每次写入先持久保存完整请求。`take` 的 begin、claim、start 分别保存：中间失败不会丢掉已成功的承接。网络结果未知时会阻止新的业务写入，`retry` 只发送原字节和原 ID；返回历史回执后再查当前事实。不要删掉 profile 或生成替代任务来绕过未决写入。

`submit` 和 `finish` 是两个独立动作。前者交付，后者结束会话；两者都不是 issuer 验收。证明丢失或换 Actor 时，需要合法 release 和重新承接；定向任务换目标还需要 issuer 显式更新 claim policy。高级 transfer、策略调整及原始协议入口见 [HTTP 参考](../skills/task-ledger/references/http.md)。

## 验证范围

本轮[便捷 Skill 验收](validation/easy-skill-results.md)已覆盖真实 HTTP 主子任务、Codex 原生派生两个执行者、33 项实际代码测试、拒绝重开第二轮交付，以及不继承历史的新主 Agent 上下文用原 profile 接续提交。父任务最终由测试 issuer 接受。

历史[四客户端验收](validation/s3-agent-acceptance.md)覆盖 Codex、Pi、Claude Code、远端 Codex 的公开协议互操作，但使用预定义子任务和外层脚本启动客户端。不能把这些历史结果当作每种宿主的便捷入口与原生派生均已重新验收。

后台自动 launcher、自动调度和长期 handoff 仍未实现；已有会话内的原生派生和 profile 接续不依赖这些能力。
