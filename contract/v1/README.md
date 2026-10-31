# S1 接入契约包

版本：`ledger-http-draft-1`。日期：2026-10-31。状态：**本项目实施基线，未实现运行服务**。上游仍固定为 `task-core-draft-1` / `e6dae9a6127537af94f0f5127e8d590a1e6a3f1a`，未修改其文件。

| 文件 | 作用 |
|---|---|
| [schema.json](schema.json) | 3种请求、响应、公开对象、配置及错误的机器结构 |
| [routes.json](routes.json) | HTTP路径与根消息定义对应 |
| [operations.json](operations.json) | 11个核心、16个应用、9个查询的输入/结果与权限摘要 |
| [errors.json](errors.json) | 核心和binding错误到HTTP状态的映射 |
| [vectors.json](vectors.json) | 正反结构样例与后续黑盒行为预期 |
| [HTTP契约](../../docs/binding.md) | 封套、权限、幂等、项目/任务应用事务、大小与错误 |
| [身份会话契约](../../docs/contracts/identity-session.md) | 注册、凭据、proof、执行绑定、会话恢复/交接 |
| [读取唤起契约](../../docs/contracts/reads-dispatch.md) | 快照水位、cursor、配置日志、Wake状态与启动边界 |

Schema用固定URN引用仓库内上游Schema；检查器禁止联网加载引用。HTTP header中的Bearer/session proof不属于正文Schema，不进入样例。配置样例使用假路径和环境变量引用：[skill](../../integrations/skill-config.example.json)、[runner](../../integrations/runner-config.example.json)。它们只说明结构，不是可安装或可启动的集成。

## 检查与证据

仓库根目录执行 `make check`，或仅执行 `go run ./tools/check-contract`。

`cases` 是独立的结构样例，valid表示应否通过JSON Schema，不表示消息在某个Task状态下可执行。每个路由操作至少有一个正例；失败结构、错误对象、会话/事件约束及配置同样验证。

`scenarios` 是有前置条件的黑盒行为规格。当前工具只核对请求结构、错误名/HTTP映射及非空不变量；**不执行步骤、不构造数据库、不判断预期状态是否已经被实现**。S01–S18涵盖原子创建、幂等冲突、session证明/交接、唤起失效/未知、快照接续/重建、角色、容量和skill部分完成；真实测试在S2–S4实现。固定游标示例是符号值，实际测试必须取上一响应返回的token，不能把样例字符串当真实签名token。

配置样例与cases中的配置应保持一致；静态校验不证明路径存在、凭据有效或执行命令安全。Go工具是契约检查器，不是生产输入解析器或服务实现。

## 版本变化

本包是S1首个实施输入。字段、操作、权限、错误含义或状态规则发生兼容性变化时，使用新的binding版本及目录；兼容性澄清也须在[变更记录](CHANGES.md)注明，并重新运行结构和受影响的行为验收。禁止静默修改上游副本替代升级。

本版保留全工作空间共享读、带外凭据/profile管理、有界完整快照、不清理历史，以及不可转移issuer等边界。采用方不要据此宣称具备公网多租户隔离、无限容量或外部执行恰好一次保证。
