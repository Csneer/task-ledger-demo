# taskledgerd 后台入口

Go 单进程服务负责加载私有配置、初始化 authority 和管理者、打开 SQLite，并提供三个 HTTP 路由。Bearer 身份映射、会话头、错误映射和公开查询均通过真实应用层处理。

配置与启动步骤见[后台接入](../../docs/backend.md)，协议范围见 [binding](../../docs/binding.md)，实际客户端证据见 [S3 验收记录](../../docs/validation/s3-agent-acceptance.md)。前端、handoff 与受控 launcher 尚未实现。
