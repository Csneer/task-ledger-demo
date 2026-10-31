# S2：Go 核心与持久事务的实施证据

日期：2026-10-31。范围：`task-core-draft-1` 的独立 Go 实现、SQLite 文件存储及受信宿主应用入口。上游固定提交为 `e6dae9a6127537af94f0f5127e8d590a1e6a3f1a`；本轮不修改协议 Schema 或语义。

S2 已具备可执行的 Go 行为与事务测试。它还没有 HTTP 服务、Bearer 凭据适配、公开签名 cursor 或真实 Agent 客户端；以下结果不能视为完整 `ledger-http-draft-1` 符合性、日常使用验收或压力容量结论。

## 实现范围

| 层次 | 已实现 | 边界 |
|---|---|---|
| 核心 | `task.create/revise/claim`、`run.start/report/submit/fail`、`task.decide`、`assignment.release`、`task.reopen/cancel`，共 11 个操作 | 只处理任务语义与事实；没有自动租约到期回收 |
| 应用动作 | `participant.create/update`、`project.create/update`、`session.begin/update/rotate/end/transfer`、`claim_policy.update`，共 10 个动作 | 其余应用动作返回不支持；没有 handoff、LaunchProfile 或 WakeRequest 执行器 |
| 可信入口 | `Service.Execute`、`Action`、`Snapshot`、`Session`、`Events`，以及一次性 `Initialize` | `Host` 由可信调用方提供；不能直接当作网络认证输入；读取水位是内部整数 |
| 准入与执行 | 注册状态、独立 read/write/admin 能力、issuer/assignee 守卫、指定 Agent、session proof、占用与显式迁移 | 管理能力不能代替 issuer；会话结束不释放承接或完成任务 |
| 持久化 | Task 快照、应用元数据、执行绑定、成功回执、事件在一次写事务中提交 | 没有历史清理、数据库备份恢复或跨 authority 迁移 |
| 输入与结构 | 精确 JSON、重复键/非法 Unicode 拒绝、离线嵌入 Schema 校验 | 使用当前固定 Schema 的数字校验假设；不提供通用任意 Schema 引擎 |

创建任务时项目关联与初始领取策略一起写入。领取时在同一事务内检查当前策略、session 资格与唯一占用，再提交核心承接事实。携带 wake 的创建请求返回 `PROFILE_UNAVAILABLE` 并整体回滚，不静默丢弃唤起要求。

成功回执按 authority、ActorRef 和请求 ID 区分。重放检查当前读取能力，返回历史结果；不会重做写入、恢复旧执行资格或将当前任务倒退。同键改变核心命令与改变应用封套分别保留 core/binding 冲突含义。

## 存储与资源约束

- 驱动为 `modernc.org/sqlite v1.61.0`，文件数据库采用 WAL、`synchronous=FULL`、每连接 foreign keys 与 5 秒 busy timeout；每个 Store 最多 4 个连接。
- 写事务使用 `BEGIN IMMEDIATE`，在读取和判断前取得写入资格；读事务中的对象与水位共享一致快照。
- schema v1 由 `objects` 与只追加的 `events` 组成。拒绝未知数据库版本、非空无版本库和内存/URI 数据库；详见[迁移说明](../../migrations/README.md)。
- 原始请求上限 1 MiB；成功响应上限 16 MiB，在提交前检查。历史持续增长可能使某次响应超限并拒绝写入，本轮没有据此声称无限历史容量。
- JSON 实现限制嵌套深度为 256。它是本实现资源限制，不是对上游协议新增的规范要求。
- 数字保留精确十进制值，幂等比较不会先转成 `float64`。为避免极端指数使通用校验库展开巨大数值，仅在结构校验副本中使用有界数字投影；原请求、存储值与回执比较保持原值。初始化检查当前 Schema 的数字约束假设，未来 Schema 引入不支持的数字规则时明确失败，不能直接复用该投影。

## 测试证据与对应场景

下面列出已经建立并通过定向运行的测试。U/R 编号是[验证计划](../validation.md)中的关联规则，并非把整个产品场景标为通过。应用测试使用真实 `Service` 和文件数据库；涉及公开客户端和网络入口的部分仍待 S3。

| 规则或故障 | 测试证据 | 可支持的结论 |
|---|---|---|
| 交付与验收分离、执行依据不变；U01/U03/U08/U09 | [核心测试](../../internal/core/transition_test.go)：`TestSubmissionRequiresIssuerDecision`、`TestHumanExecutionAndAgentIssuer`、`TestRevisionsPreserveOriginalExecutionBasis`、`TestFailureAndRejectionAreDistinctAndReopenPreservesFacts` | 人和 Agent 的核心规则均可执行；原执行依据与历史事实保留。尚无真实人的操作入口验收 |
| 管理凭据能力 | [应用测试](../../internal/application/service_test.go)：`TestAdminActionsRequireCredentialCapabilityAndOperatorIdentity` | 同一管理者身份缺少 admin 或 write 时不能管理目录，普通身份自带 admin 标记也不能取得管理者资格 |
| 生命周期、持久回执；U01/U10、R02 | [应用测试](../../internal/application/service_test.go)：`TestDurableHumanAgentLifecycleAndReplay` | 创建、领取、开始、汇报、交付、验收后重开数据库，旧提交回执可读且当前任务仍为 completed |
| 并发唯一领取与重复请求；R01/R02 | 同文件：`TestConcurrentClaimIsOneWinnerAcrossIndependentStores`、`TestSameCommandConcurrentReplayAcrossIndependentStores` | 两个独立 Store 竞争同一任务只有一个成功领取；并发同请求只产生一次成功事实 |
| 精确等价、错误作用域；R03 | 同文件：`TestStrictCommandInputAndNumberEquivalentReplay`；[独立反例](../../internal/application/adversarial_test.go)：`TestAdversarialCoreAndApplicationIdempotencyScopes` | 不把精确小数舍入为另一请求；数字等价与 core/binding 冲突分别处理 |
| 旧执行、停用与定向竞争；R04/R06/R07 | 应用测试 `TestCoreSemanticsReviseRejectRetryAndStaleAttempt`；独立反例 `TestAdversarialReadonlyRevocationAndReceipts`、`TestAdversarialTargetPolicyAndConcurrentClaims` | 旧承接不能继续写，新写与历史读取资格分开；策略更新与领取串行判断 |
| 会话归属与迁移；R12/R17 | 应用测试 `TestSessionRotationTransferAndEndedPresence`；独立反例 `TestAdversarialSessionProofAndErrorOrder`、`TestAdversarialRotatedProofAndTransferCannotStealAssignment` | 会话结束不改 Task；旧 proof、其他 session 或其他 Actor 不能接管当前执行 |
| 应用提交后进程退出；R05 | [恢复测试](../../internal/application/recovery_test.go)：`TestApplicationCommittedClaimSurvivesProcessExit` | 子进程经真实 Service 提交 claim 后直接退出、不调用 Close；重开后 Task、binding、receipt 与事件保留，重试不新增事实 |
| 应用最终写入失败；R10 | 同文件：`TestApplicationReceiptFailureRollsBackClaimFacts` | 测试 trigger 让最后的 command receipt 插入失败；Task、metadata、水位、binding、占用与 receipt 整体回滚，同请求可重新提交 |
| 存储提交前/后进程退出与跨进程竞争；R01/R05 | [存储测试](../../internal/storage/sqlite_test.go)：`TestSubprocessCrashBeforeAndAfterCommit`、`TestSubprocessCompetingWriters` | 真实子进程中断验证存储事务全有或全无；此项不是应用命令执行中途退出的替代声明 |
| 一致快照、取消与连接恢复；R08/R10 的底层部分 | 同文件：`TestViewHasConsistentObjectsAndWatermarkAcrossConcurrentCommit`、`TestCancelledContextRollsBackAndConnectionRemainsUsable` | 存储视图与水位一致，取消可回滚；尚未验证公开 cursor 的断线恢复 |
| 响应容量与未实现唤起；R10/R14 的拒绝路径 | 应用测试 `TestResponseCapacityRejectionRollsBackAllRows`；独立反例 `TestAdversarialUnavailableWakeRollsBackCreate` | 16 MiB 响应限制与 wake 不可用都不产生部分事实；没有证明真实 launcher 副作用 |

JSON 与嵌入 Schema 测试另覆盖非法 UTF-8、孤立代理项、重复键、深度边界、极端数字、数字投影约束和全部 114 个结构向量。测试运行不依赖当前工作目录读取协议文件，也不联网取 Schema。

## 验证记录

环境：Go 1.27.0，Linux/amd64，内核 7.0.0-31-generic。数据均为测试生成的临时文件数据库与合成身份；没有真实 Agent 凭据或私人任务。

| 检查 | 当前证据 |
|---|---|
| 静态契约检查 | `go run ./tools/check-contract` 通过：103 binding / 31 upstream 定义、114 结构向量、36 操作正例；18 行为规格仍只校验形状 |
| 定向运行检查 | 核心、存储、精确 JSON/Schema、应用并发与独立反例的定向测试通过；应用恢复测试在普通与 race 模式通过；响应容量回滚测试通过 |
| 最终整套检查 | `make check` 通过：格式、上游完整性、静态契约、`go test -race ./...`、`go vet ./...` 与 `go build ./...` 均通过；其中应用包 race 测试含长历史容量场景，耗时 66.578 秒 |
| CI 配置检查 | actionlint v1.7.7 通过；GitHub 托管 CI 结果须在推送后另行核对 |
| 独立代码审查 | 通过；发现的 admin 凭据能力缺口已修正并加入回归，没有遗留阻断项 |

`tools/check-contract` 输出中的 “NOT RUN” 指该静态工具自身没有执行的行为，不能据此忽略上表独立 Go 测试；同样也不能把静态工具通过误报成 HTTP 行为通过。

## 尚未证明的能力

- 完整 HTTP 路由、Bearer 撤销与认证错误、签名 cursor、公开查询恢复，以及两个独立客户端的互操作。
- 可安装 skill、组合步骤的客户端恢复、真实人验收、显式 handoff 和持续日常使用。
- 启动配置、WakeRequest/outbox、外部进程启动与重复/未知启动结果处理。
- 磁盘断电、损坏、备份恢复、空间耗尽和完整过载恢复；进程退出测试只覆盖对应进程故障。
- L01–L10 吞吐、延迟、资源和长期历史增长基线。并发正确性测试不是压测结果，也不构成 QPS 承诺。

下一阶段按[路线图](../roadmap.md)进入 S3，通过真实 HTTP 和独立客户端验证已实现规则是否足以支撑使用，并将协议问题、实现缺陷与容量问题分别记录。
