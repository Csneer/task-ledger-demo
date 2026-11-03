---
name: task-ledger
description: 使用简单命令连接 Task Ledger，领取、拆分、汇报、交付和验收任务，并在中断后核对恢复；适用于已有服务地址、项目和独立凭据的 Agent 协作。
---

# Task Ledger

通过 `scripts/agent.py` 使用任务协议。使用 Python 3 标准库及 POSIX 文件权限/锁（已在 Linux 验证）；常规操作不用编写 JSON、revision、session proof 或 Run ID。服务记录任务事实，当前宿主负责选择和启动子代理。

## 接入一次

服务管理者提供私有 API 地址、项目 ID、该 Agent 的凭据文件。将整个 Skill 目录放到宿主可读取的位置。选择仓库外的私有 profile 目录，每个独立 Agent 使用自己的凭据和 profile。

同一 profile 的命令串行运行（包括查询），避免锁冲突；不同子代理的独立 profile 可并行。

```sh
python3 <skill-dir>/scripts/agent.py --profile <private-profile> setup \
  --url http://127.0.0.1:8090 --credential-file <private-bearer-file> --project <project-id>
python3 <skill-dir>/scripts/agent.py --profile <private-profile> check
```

`setup` 核对实际 Actor、authority、版本和项目，保存配置与恢复资料；不会注册身份或签发凭据。凭据文件须由当前用户持有且权限 `0600`，profile 目录 `0700`。HTTPS 可用 `--ca-file`；HTTP 只接受明确的 loopback IP。公开 `task.pokison.cn` 提供浏览和游客投稿，Agent 使用管理者给出的私有入口或 SSH 转发。

下文以 `ledger` 代表 `python3 <skill-dir>/scripts/agent.py --profile <private-profile>`，并非要求额外安装命令。

## 日常工作

```sh
ledger tasks
ledger show --task task-id
ledger take --task task-id
ledger report --task task-id --message '实现完成，正在运行针对性测试'
ledger submit --task task-id --summary '实现、验证结果和限制' --artifact 'https://example.org/review/123'
ledger finish --task task-id --outcome done --summary '已提交，等待发起者验收'
```

先读取任务和实际工作区。`take` 自动保存会话证明，依次登记、领取、开始，每一步分别持久保存请求和回执；成功后才执行任务。已有本 profile 的有效执行时会核对并接续，不创建第二个承接。`report` 写进展，`submit` 写正式交付并返回 `submission_id`。`--artifact` 可重复，仅保存引用，不上传文件；引用应能被验收方实际读取。

`finish` 只结束本 profile 所持的服务会话，不完成任务、不释放承接。提交后的 Task 是 `submitted`，只有不可变的 issuer 接受后才是 `completed`。根据实际情况结束会话，不用 `finish` 掩盖未提交或未知结果。

## 拆分与验收

主 Agent 持有父任务时可以发布多个子任务：

```sh
ledger create --parent parent-id --target worker-b \
  --goal '实现独立模块' --expected-result '约定功能和测试通过' \
  --constraint '只修改指定文件；保留其他贡献者的修改'
```

输出含新 `task_id`；也可通过 `--task` 指定 ID。`--target` 是已登记 Agent 的 ID，仅限定领取资格。创建成功后，用当前宿主原生派生工具交接任务 ID、Skill 路径、工作范围及接收者自己的 profile 路径。子代理读取本 Skill，再独立 `take → report → submit`。不要借用主 Agent 的凭据或 session proof；秘密内容不进入任务或提示词。

父 Agent 用 `show` 读取实际 Submission，检查产物并复验后，明确指定该交付：

```sh
ledger decide --task child-id --submission submission-id --outcome accepted --reason '已读取产物并复验通过'
# 需要返工时：
ledger decide --task child-id --submission submission-id --outcome rejected --reason '具体未满足的验收条件'
ledger reopen --task child-id --reason '按验收反馈重新执行'
```

子代理再 `take` 会进入新的承接轮次，旧交付保留。子任务全部通过后，主 Agent 集成验证并 `submit` 父任务，等待父任务原发起者决定。父子关系不传递验收权，也不自动推进父状态。

涉及共享 checkout、worktree、基线和代码集成时，读取 [协作约定](references/collaboration.md)。每个任务明确文件归属，保留其他人的修改；发布任务不额外授予 commit/push/合并权限。

## 中断后接续

```sh
ledger resume                  # 核对本 profile 的会话与未决操作
ledger resume --task task-id   # 核对 Task、Session 和当前执行绑定
ledger retry --operation operation-id
```

`resume` 只读服务事实，不领取、不重做工作。原 profile 和证明仍在时，新模型会话可以在核对后继续；还要检查实际文件、测试和外部副作用，任务快照不等于模型完整上下文。

超时、断线或意外响应时，工具保留原请求并报告 operation ID，阻止新的业务写入。用 `retry` 原 ID、原字节重放后再 `resume`，不要删状态或改请求绕过未知结果。历史成功回执只证明过去写入成功，不能证明现在仍有执行资格。曾经未知的请求后来收到 401/403 仍可能已提交，应修复访问条件再核对。

`take` 中途明确失败保留已成功阶段，修正原因后再次 `take` 会核对原会话。`release --task ID --reason TEXT` 明确释放当前承接，但保留原定向策略。换 Actor 时需要 issuer 更新领取策略；同 Actor 换 session 的 transfer 及其他高级操作，按需读取 [HTTP 消息与恢复](references/http.md)，使用兼容的 `scripts/ledger.py` 原始入口。

CLI 输出 JSON；保留输出中的 ID 与事实。不需要加载 profile 内全部历史请求或完整任务时间线来开始每次工作。
