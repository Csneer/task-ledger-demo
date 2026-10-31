# Task Ledger Skill

可将本目录整体复制到 Agent 的 skills 目录，或直接读取 [SKILL.md](SKILL.md)。实际接入需要管理员提供服务地址、稳定身份对应的凭据，以及已登记的项目。

[标准库 HTTP 助手](scripts/ledger.py)按原始 JSON 调用公开接口，写前持久保存请求意图；它不依赖服务内部 Go 包或数据库。详细消息见 [HTTP 参考](references/http.md)。唤起 Agent、自动调度和前端不在本 Skill 范围。

脚本回归：`PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s skills/task-ledger/scripts -p 'test_*.py'`（仓库根目录）。这些临时 HTTP fixture 测试只证明客户端保存、传输和失败处理行为；真实服务/Agent 互操作须另行验收。
