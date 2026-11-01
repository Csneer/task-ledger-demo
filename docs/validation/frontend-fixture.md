# 前端与本机 Codex 的隔离任务服务

`scripts/frontend_fixture.mjs` 使用 Node.js 20+ 标准库启动已编译的 Go daemon，通过公开 HTTP 创建项目、参与者和任务。它不启动 Codex，不模拟报告，不自动验收。服务与数据库只属于指定的私有目录。

```sh
go build -o /tmp/taskledgerd-frontend ./cmd/taskledgerd
node scripts/frontend_fixture.mjs \
  --daemon /tmp/taskledgerd-frontend \
  --dir /tmp/task-ledger-frontend-session
```

目录须尚不存在或为空，父目录须已存在；非空目录直接拒绝，避免覆盖已有任务。可追加 `--port 8090`；省略或传 `0` 时选空闲 loopback 端口。只监听 `127.0.0.1`，不更改网络、代理或全局 Agent 配置。

stdout 第一行是机器可读 ready JSON，字段包括 `url`、`dir`、`ownerTokenFile`、`codexConfig`、`projectId`、`parentTaskId`、`agentTaskId`。其中只有配置和凭据的文件路径，没有凭据内容；常规进度与错误写 stderr。`fixture.json` 保存同一份路径和任务信息，`daemon.log` 保存 daemon 日志。目录权限为 `0700`，配置、凭据、意图及日志文件为 `0600`，不得提交到 Git。

初始身份与任务：

- 人 `human/owner`：初始管理者与这些任务的发起者，可以真实验收；Bearer 文件见 `ownerTokenFile`。
- 本机 Codex `agent/codex`：已登记的普通读写 Agent，没有管理权限；Skill 客户端配置见 `codexConfig`，私有 Bearer 在同目录 `codex-token`。
- `frontend-build`：当前真实前端开发的总任务。
- `frontend-review`：定向给 Codex 的“前端交互与协作约定验收”子任务，要求检查实际代码并报告证据。
- `frontend-tooltip-review`：更深一层的悬停/详情样例任务。
- `frontend-motion-a`、`frontend-motion-b`：关联到总任务的两个独立并行方案，使用 namespaced 扩展显示分组。

所有任务初始都是 `offered`，没有 Assignment、Run、Report、Submission 或 Decision。最后三个在任务正文与扩展中标记为未执行的合成样例；它们用于观察深层关系和多开布局，不构成实际工作成果。`frontend-review` 的后续领取与交付必须由真实客户端完成。

当前会话需要派生子 Agent 验收时，将 TaskRef、仓库位置、Skill 位置和 `codexConfig` 路径交给子 Agent；不要把 Bearer 内容写进提示词。接收者按 [Task Ledger Skill](../../skills/task-ledger/SKILL.md) 创建独立 session、主动领取 `frontend-review`、读取实际前端并报告、提交交付。父会话或人使用 owner 身份核对产物后决定接受/拒绝。所有初始任务均由 owner 发起，因此 Codex 不能代为验收；子任务完成也不会自动完成总任务。

按 Ctrl-C 或向 fixture 进程发 SIGTERM，它会对自己启动的 daemon 发 SIGTERM 并等待优雅退出，超时才对该子进程发 SIGKILL。数据库、凭据和意图文件全部保留。再次查看原任务时，直接运行已有 daemon 配置，不重复初始化或重新播种：

```sh
/tmp/taskledgerd-frontend -config /tmp/task-ledger-frontend-session/server.json
```

若启动或写请求中断，先检查日志和保留的原意图，再使用相同请求 ID 核对未知结果；不要删除目录重建来掩盖部分成功。测试结果应分别记录“seed 布局”“真实 Codex 领取交付”“owner 验收”，不能把前者当成后两项通过。

## 百任务可读性夹具

在同一个明确隔离的 loopback fixture 上，可以通过公开接口追加固定的 `ux-100` 项目：

```sh
node scripts/usability_fixture.mjs /absolute/private-fixture-directory
node scripts/usability_e2e.mjs /absolute/private-fixture-directory
```

播种脚本只接受 loopback HTTP，核对客户端与服务监听地址、authority 和协议版本。它为 100 项任务创建 20 条显式关系链，每链 5 项，涵盖八种核心状态。每次写入前保存原请求，遇到既有项目或意图目录会拒绝重复播种，不清理或篡改既有任务。

这些任务明确标为合成验收数据。所有状态均通过协议命令产生；构造结束后 session 全部结束，保留 claimed/active 仅用于检查静态状态的显示，不代表有 Agent 进程仍在工作。

`usability_e2e.mjs` 使用浏览器读取该项目，检查分页全集、真实节点范围、跨任务自动跟随、手动镜头保护、表单暂停、独立单任务回放和移动端可读性。它不发送写命令，并比较前后的全部任务状态及 revision。JSON 和截图保存在所指定的私有目录；浏览器截图需等待节点入场动画可见，不能仅凭 DOM 节点计数宣称视觉验收。
