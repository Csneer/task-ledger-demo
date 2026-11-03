# 便捷 Skill 验证记录

日期：2026-11-03。接口沿用 `ledger-http-draft-1` / `task-core-draft-1`，上游锁定未变化。此次新增的是 Python 标准库客户端 `agent.py`，服务权限和核心状态机没有新增语义。

## 自动化验证

`make check` 已通过契约校验、Go race 测试、vet/build、Skill 客户端测试、初始化与公开站适配测试，以及 46 项前端模块测试。新增 `make skill-smoke` 也纳入这一入口，之后每次 CI 都会启动隔离 Go daemon 验证便捷命令。

客户端共 27 项测试（原始客户端 7 项，便捷客户端 20 项），覆盖：

- 配置核对、私有文件权限、profile 锁、Actor/authority 漂移拒绝。
- 写前原子保存完整请求与操作元数据；未发送的临时目录不阻塞接续。
- 服务已提交但响应丢失后，在新客户端实例原样重试，只产生一次事实。
- 未知写入阻止新写；原字节/原 ID 重放；随后收到 403 仍保持未知。
- 截断响应返回可恢复的 operation ID；大于 1 MiB 的合法回执可重新读取。
- begin/claim/start 分步持久化；start 明确拒绝后继续使用原 session 和承接。
- `resume` 只读；同 Actor 的另一个 session 不能冒用执行归属。
- submit 使用当前 Run 的 basis 和当前 Task revision；decide 不切换显式指定的 Submission。
- parent/target 投影正确，create 不自动领取。

传输替身测试证明客户端恢复逻辑；真实 HTTP smoke 补充证明与 Go 服务的契约兼容：

```sh
make skill-smoke
# 已构建 daemon 时也可直接运行：
python3 scripts/easy_skill_smoke.py --daemon /absolute/path/taskledgerd
```

服务和四个身份均位于独立临时目录。只有管理 bootstrap 创建参与者和项目；Task、Session、Assignment、Run、Report、Submission 和 Decision 全部由 `agent.py` 经公开 HTTP 生成。父任务和两个子任务最终完成，其中一个子任务经历拒绝、重开、第二 generation/Run/Submission 和接受，历史记录保留。daemon 正常重启后，`resume` / `take` 保留父任务原 session 与 Run，没有重复领取。每次测试停止自己启动的 daemon，私有目录中保留脱敏 `report.json` 和诊断资料。

## 原生子代理与新主会话接续

另开隔离服务，测试 issuer 只给主 Agent 一项完整目标：编写标准库 Python 交付清单校验器，核对 SHA-256、拒绝越界/符号链接/重复路径、提供 JSON CLI、退出码、README 和自动测试。主 Agent 自行决定分成核心与 CLI 两项任务，通过高层命令发布，再使用宿主 `collaboration.spawn_agent` 启动两个真实执行者，文件归属互不重叠。

所有业务流转使用同一 Skill 和 `agent.py`。四个身份预先独立配置，执行者没有借用主 Agent 的凭据。两个子代理分别领取、汇报、提交和结束会话；主 Agent 读取实际代码和 Submission 并独立运行测试后作决定。

| Task | 最终 generation | 交付与验收证据 | 最终状态 |
|---|---:|---|---|
| `native-code-parent` | 1 | `submission-7` → `decision-8` accepted | completed |
| `native-manifest-core` | 1 | `submission-5` → `decision-6` accepted | completed |
| `native-manifest-cli` | 2 | `submission-6` → `decision-7` rejected；`submission-12` → `decision-13` accepted | completed |

CLI 第一轮的 32 项组合测试已通过。为验证返工，主 Agent 明确记录了一次**计划内补充验收演练，并未声称原实现有缺陷**：要求增加 Unicode/空格路径、超过 2 MiB 的二进制文件，以及在第 1 MiB 之后篡改字节的回归测试。拒绝后显式 reopen，再用 `collaboration.followup_task` 接续该子代理。第二次 take 产生新 generation、Assignment 和 Run；补充后 33 项测试通过，新交付得到接受，旧交付与拒绝记录保留。

第一位主 Agent 完成子任务集成后，只写报告和简短 `HANDOFF.md`，保留父任务 active，没有提交或结束父会话。随后启动**不继承历史的新主 Agent 上下文**，仅交给 Skill、交接文件和原 profile 路径。它先 `resume` 核对，再读取必要事实、代码和测试，独立复跑 33 项测试后提交父任务并 finish。

接续前后保持同一个 session、`assignment-2`、`run-3` 和 generation 1，没有新建替代父任务或重新领取。最终测试 issuer 再读取实际父交付、复验 33 项测试并接受，主会话为 ended，未决操作为 0。精确引用、产物 SHA-256 与边界见[脱敏证据 JSON](easy-skill-evidence.json)。

实际使用中，新主 Agent 曾并行执行同 profile 的两个查询，第二个被本地锁拒绝；串行重试正常。根据这项实测反馈，Skill 明确写出同 profile 串行、不同 profile 可并行。

## 证据边界

本轮证明了 **Codex 原生子代理 + 便捷 Skill + 真实代码交付 + 明确返工 + 新主上下文接续**。上下文交接是计划内切换，不冒充自动模型重启或强杀崩溃恢复；后者的部分写入恢复由故障注入测试单独覆盖。

Claude Code、Pi 可调用相同标准库客户端，历史协议互操作已有证据，但本轮没有重新执行它们的原生子代理流程。更深递归树、长期无人值守、自动 launcher/调度和长期 handoff 均不在本轮通过声明内。代码练习的原始文件保留在私有临时工作区，不是本产品新增的功能；提交仓库的是脱敏事实和校验摘要。公开演示站与生产数据不参与本轮测试。

凭据、session proof、数据库、完整模型 transcript 均不提交仓库。测试中的 human issuer 是受控测试身份，不代表真实用户已经阅读并批准交付。
