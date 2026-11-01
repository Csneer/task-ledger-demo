# 真人验收：Task Ledger Developer Preview

这份流程用于维护者和试用者完成一次真实的人机闭环。v0.1.0 已获维护者授权作为 Developer Preview 发布，真人日常使用验收仍待完成；发布授权不替代具体任务的 issuer 决定，也不把自动化测试、Agent 的 `run.submit` 或聊天汇报当作人的产品验收。

1. 从干净 checkout 按 [后台接入](backend.md) 构建候选二进制，使用私有 `0700` 运行目录和 `0600` 配置/凭据；确认 `taskledgerd -version`、`service.info` 和非 loopback TLS 边界。
2. 用 owner 身份打开 Task Orbit，发布一个带明确预期结果的任务；核对任务、项目和 issuer。
3. 由当前会话派生一个 Agent。只交给它公开 TaskRef、Skill 配置位置和工作区，不交凭据文本；Agent 使用自己的 session 主动 `claim → start → report → submit`。
4. 在页面打开详情，阅读报告和正式交付；在独立的验收测试任务中覆盖一次退回/重做和相关子任务，确认旧交付和新交付分别保留。真实工作是否接受仍以实际结果为准。
5. 以 owner 身份核对真实交付后执行 `accept` 或 `reject`，确认只有 issuer 能作决定；页面刷新后历史顺序和任务状态仍一致，已有历史仍可读取。
6. 停止并按 [运维说明](operations.md) 做一次隔离冷备/恢复 smoke；恢复实例只通过公开接口核对快照、回执和 cursor，不与原实例并行。
7. 记录候选提交、浏览器/服务地址、验证命令、未覆盖边界和是否接受 `frontend-build`。接受页面交付不等于批准未实现的 launcher、handoff、跨 authority 或公网能力。

若任一步依赖未知凭据、未验证的远端网络或未完成的恢复演练，应把该项记录为待处理，不把开发者预览标为已经完成真人验收或适合生产使用。

当前已知的真实前端开发任务 `frontend-build` 在 [2026-11-01 前端验收记录](validation/frontend-results.md) 中为 `submitted / revision 5`。请先读取服务中的最新交付再决定是否接受；候选构建或自动化检查通过不会替它作决定。
