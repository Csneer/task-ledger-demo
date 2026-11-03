# Task Ledger Skill

可将本目录整体复制到 Agent 的 skills 目录，或直接读取 [SKILL.md](SKILL.md)。实际接入需要管理员提供服务地址、稳定身份对应的凭据，以及已登记的项目。

[便捷客户端](scripts/agent.py)提供 `setup / take / report / submit / decide / resume` 等命令，自动管理协议字段、会话与恢复检查点。先按 [SKILL.md](SKILL.md) 配置一次，再用任务 ID 工作，无需手写 JSON。

[原始 HTTP 助手](scripts/ledger.py)继续支持完整封套和高级操作。两个入口均只依赖 Python 标准库，不依赖服务内部 Go 包或数据库。详细消息见 [HTTP 参考](references/http.md)。子代理由当前宿主启动，服务保存各执行者独立领取和交付的事实。

脚本回归：`PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s skills/task-ledger/scripts -p 'test_*.py'`（仓库根目录）。这些临时 HTTP fixture 测试只证明客户端保存、传输和失败处理行为；真实服务/Agent 互操作须另行验收。

`make skill-smoke` 启动隔离 Go 服务，通过便捷 CLI 验证四身份主子任务、拒绝重做与重启接续，保留私有临时目录中的脱敏报告，并停止本次服务。它也包含在 `make check` 中；原生模型子代理验收单独记录，不由该脚本模拟。
