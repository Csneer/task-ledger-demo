# 后台与 Skill 接入

当前后台使用 Go、SQLite/WAL 和 `ledger-http-draft-1`，适合本机或受控协作试用。前端暂缓。支持范围与真实验收结果见 [S3 验收记录](validation/s3-agent-acceptance.md)；以下配置包含占位符，必须替换为自己的私有路径和带外凭据摘要。

## 配置与启动

从仓库根目录构建：

```sh
go build -o /tmp/taskledgerd ./cmd/taskledgerd
/tmp/taskledgerd -version
```

在仓库之外建立权限 `0700` 的运行目录，保存权限 `0600` 的 `server.json`。配置中的数据库和 TLS 路径相对配置文件解析；数据库父目录必须已存在。

```json
{
  "authority": "personal",
  "database": "ledger.db",
  "listen": "127.0.0.1:8090",
  "operators": [{"kind": "human", "id": "owner"}],
  "credentials": [
    {
      "digest": "<sha256-hex-of-decoded-credential>",
      "actor": {"kind": "human", "id": "owner"},
      "read": true,
      "write": true,
      "admin": true
    }
  ]
}
```

每个凭据使用独立的 32 字节加密随机值，Bearer 文本是这些字节的无填充 base64url；`digest` 是**解码后的原始字节**的 SHA-256 小写 hex，不是 Bearer 文本的摘要。服务配置只保存摘要。原始 Bearer 通过私有文件或秘密渠道交给对应客户端，不放入仓库、任务正文或日志。

首次初始化会建立 authority、管理者身份和事件。`-init` 只初始化并退出；已有数据库再次初始化会拒绝，不会覆盖历史：

```sh
/tmp/taskledgerd -config /private/path/server.json -init
/tmp/taskledgerd -config /private/path/server.json
```

将 `/private/path/server.json` 替换为实际配置路径。正常重启只运行第二条命令。启动日志包含监听地址和构建标识；使用合法凭据查询 `service.info` 作为就绪核对，不把端口占用当成目标服务就绪。SIGINT/SIGTERM 会触发最多 10 秒的优雅停止。

默认地址为 `127.0.0.1:8090`。非 loopback IP 必须同时配置 `tls_cert` 与 `tls_key`；客户端必须正常校验证书。远程验收计划使用加密 SSH 隧道连接 loopback HTTP；本轮实际 SSH 连接超时，未形成跨主机或直接 LAN HTTPS 通过证据。

这些构建、初始化和启动参数已由源码及本轮真实后台启动核对；配置中的占位摘要不能直接运行。完整运行结果、清理和未覆盖项以验收记录为准。

## 身份登记与凭据更新

1. 管理者通过 `participant.create` 登记稳定的 `{kind,id}`。显示名、模型和能力资料仅用于展示，不授予任务权限。
2. 在服务的 `credentials` 中加入该 ActorRef 的凭据摘要及独立 read/write/admin 能力，带外分发原始 Bearer。
3. 重启服务加载新的凭据快照。删除或替换摘要也在重启后生效；当前没有热重载、网络注册发证或登录界面。
4. 使用该凭据查询 `workspace.snapshot` 核对项目和目录，再登记实施会话、领取任务。

`operators` 用于首次初始化的管理者名册，修改配置中的该数组不会修改已存在的数据库管理者身份。管理操作同时要求有效管理者登记和凭据 admin/write 能力。管理员不能代替任务 issuer 验收；共享工作空间的 read 凭据可读取所有公开任务，指定 Agent 仅限制领取资格。

## HTTP 与查询边界

| 入口 | 已实现操作 |
|---|---|
| `POST /v1/commands` | 11 个核心操作，包括创建、领取、执行、交付和决定 |
| `POST /v1/actions` | participant、project、session 和 claim_policy，共 10 个动作 |
| `POST /v1/queries` | `service.info`、`task.snapshot`、`workspace.snapshot`、`events.open/read`、`session.get`、`profiles.list` |

请求需 UTF-8 JSON、Bearer 及相应会话头。完整封套、错误状态和权限见 [binding](binding.md)。请求上限 1 MiB，完整响应上限 16 MiB；workspace 各类对象上限 1000，事件页上限 200。超限明确拒绝，不截断成功数据。

watermark 与 cursor 是不透明签名值。cursor 绑定当前 ActorRef，正常重启后可继续使用；读取时仍检查当前权限。分页按客户端返回的 next/follow cursor 推进，消费者按 event_id 去重。当前无历史清理或备份恢复流程，不能通过复制旧数据库宣称旧 cursor 仍有效。

`handoff.put`、唤起和 runner 调度尚未实现，相关操作明确返回不支持；`profiles.list` 返回当前真实空目录。含 wake 的创建请求明确拒绝并回滚。没有自动租约到期回收、任务正文执行器或外部工作停止保证。

## Skill 客户端

将 [skills/task-ledger](../skills/task-ledger/README.md) 整个目录复制到客户端的 skills 目录，或显式读取其中的 `SKILL.md`。助手只需 Python 3 标准库，不依赖服务内部 Go 包或数据库。

在客户端私有目录保存 `0600` 配置，例如：

```json
{"service_url":"http://127.0.0.1:8090","credential_file":"bearer","intent_dir":"intents"}
```

`bearer` 是该客户端的私有凭据文件；相对路径按配置文件解析。HTTPS 可通过 `ca_file` 指定受信 CA。session 文件与 intent 目录同样保持私有权限。准备一个无副作用查询文件：

```json
{"binding_version":"ledger-http-draft-1","operation":"service.info","body":{}}
```

以仓库中的助手为例：

```sh
python3 skills/task-ledger/scripts/ledger.py query --config /private/path/client.json --request /private/path/info.json
```

路径是需替换的示例。助手原样发送请求字节，写操作在发送前持久保存原请求和幂等 ID；不自动重试、不跟随重定向，也不把多个请求包装成一笔事务。会话证明生成与完整工作链路见 [Skill](../skills/task-ledger/SKILL.md)。

## 验收与限制

真实 Agent 验收脚本为 [agent_acceptance.py](../scripts/agent_acceptance.py)，需要已认证且被明确授权使用的 CLI 环境；它不会在普通 CI 中自动调用模型。它启动隔离后台、使用合成任务并记录进程归属与清理结果。运行实际模型会使用客户端既有的账号与计算资源，应按自己的环境选择参与方。

本轮验收覆盖范围见 [S3 记录](validation/s3-agent-acceptance.md)。真人日常 review、长时间连续使用、压力容量、备份恢复、受控 launcher 和前端仍需后续证据。
