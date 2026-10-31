# ledger-http-draft-1 接入契约

状态：S1 实施基线，2026-10-31。这是本 demo 的 HTTP binding，不是上游新增的核心协议，也不是当前已有的运行 API。前端仍待定，业务服务尚未实现。

本基线由[机器结构](../contract/v1/schema.json)、[路由映射](../contract/v1/routes.json)、[操作映射](../contract/v1/operations.json)、[错误映射](../contract/v1/errors.json)、本页及两份专题契约共同定义；发现冲突必须修订契约，不由实现私选其一。[结构向量与行为预期](../contract/v1/vectors.json)分开记录，行为预期本轮不执行。

## 1. 网络封套与接口

| HTTP | 请求定义 | 成功结果 | 用途 |
|---|---|---|---|
| POST /v1/commands | CommandRequest | CommandSuccess | 原样核心命令与应用伴随参数 |
| POST /v1/actions | ActionRequest | 对应 Result_<operation> | 身份目录、项目、会话、定向与唤起操作 |
| POST /v1/queries | QueryRequest | 对应 View_<operation> | 服务能力、快照、事件、会话与唤起查询 |

operation 中的点在定义名中用下划线替代。所有响应使用 JSON，成功 HTTP 200，失败使用 ErrorResponse；没有 JSON-RPC batch/notification。仅接受 UTF-8 application/json，响应 Cache-Control: no-store。非 POST 返回405，未知路径404，错误媒体类型415，超大请求413；这些传输拒绝不产生业务回执。正文上限1 MiB，响应上限16 MiB；不可截断成功数据，超过容量用507/CAPACITY_LIMIT。

```text
command: {binding_version, command:<upstream Command>, application:{...}}
action:  {binding_version, request_id, operation, expected_revision, body}
query:   {binding_version, operation, body}
```

binding_version 固定 ledger-http-draft-1。未知字段拒绝；缺失与 null 区别，默认值不隐式补进等价比较。ID和数字沿用核心的精确JSON规则；不得先把number变成float64再判断幂等。Schema仅验证结构；重复键、非法Unicode、非有限值和越界原始数字须在运行解析器另行拒绝。

## 2. 可信身份与权限

使用 Authorization: Bearer <opaque credential>，由管理者带外配置和分发。服务仅保存高熵凭据的SHA-256摘要及映射，凭据至少32随机字节，配置限制本机读取权限。Actor凭据映射稳定ActorRef及read/write/admin能力；runner凭据只映射runner_id与dispatch权限，不是Agent ActorRef。

API不签发凭据，也不接受HostContext。首次管理者由部署配置预登记；participant.create登记后，再带外配置凭据。身份注册成功不意味着凭据已发放。ActorRef、显示名、profile都不是认证证明。

首版是共享工作空间：任何有效read Actor凭据可读取全部项目、任务和公开目录。**指定Agent是领取资格，不是保密或项目ACL。** write不自动推导read；admin需同时有write才可修改目录，不能因此获得Task issuer权限。read_only主体禁止新写但可读；完全吊销凭据返回401。轮换保持ActorRef，旧凭据撤销后不能读取历史回执。

跨网络使用TLS或受控TLS代理；本机调试可用loopback HTTP，不开放匿名自报身份模式。这属于本binding，不修改核心协议。会话证明与恢复见[身份会话契约](contracts/identity-session.md)，runner与启动见[读取唤起契约](contracts/reads-dispatch.md)。

## 3. 核心命令与事务

11个核心operation、revision、错误优先级和生命周期保持上游语义。命令须引用本服务authority，否则AUTHORITY_MISMATCH；外部TaskRef只作关系/资源声明，不向本服务写另一authority的任务。

| operation | application精确字段 | 规则 |
|---|---|---|
| task.create | project_id、claim_policy，可选wake={profile_id,profile_revision} | 项目存在；policy是workspace或target；target是enabled已登记Agent；wake只配合匹配target |
| task.claim | session_id | 与会话header一致；同actor/项目、active且无当前承接；原子建立执行绑定 |
| 其余9个 | 空对象{} | run.*要求当前会话；release的issuer路径可无执行会话 |

create的Task、项目关联、初始policy revision=1、可选WakeRequest、核心事实/事件和完整回执同事务提交。profile/目标无效、容量不足或有未决唤起时，含wake的create**整笔拒绝**。仍要只发布任务时，调用者明确发新的不含wake的意图。

写入在提交前计算完整成功响应的UTF-8编码大小（包括核心事件和应用结果）；超过16MiB返回CAPACITY_LIMIT且不提交。不能先成功提交再以响应超限507伪装成未执行。成功回执保留当时受限的响应，重放仅改变replayed标记；内部序列化错误在提交前发现。网络发送失败仍属于结果未知，不能回滚已提交事实。

CommandSuccess={binding_version,result:<upstream Result>,application:{task_meta,wake}}。wake仅为本次create创建的请求，否则null；task_meta是历史提交时的应用快照。result.replayed是唯一重放标记，旧结果不能当最新查询。

固定处理次序：认证及结构→成功回执与当前read权→新写准入→核心存在/revision/角色/归属/状态/basis守卫→应用政策与会话守卫→原子提交。核心阶段只计算无副作用候选结果，应用拒绝不得留下核心写入、事件或回执。核心错误与应用错误不互改。

## 4. 应用操作与revision

字段和结果名以operations.json及Schema为准，下表定义行为。

| operation | expected_revision目标 | 守卫与结果 |
|---|---|---|
| participant.create/update | 目录revision，create=0 | enabled admin+write；不复用ActorRef，update完整替换资料/状态 |
| project.create/update | Project revision，create=0 | create为enabled writer，created_by=真实actor；update仅creator或admin；不删项目 |
| handoff.put | 项目Handoff revision，首次=0 | enabled writer；完整替换summary/next_actions/sources，保留历史；源Task须本authority同项目且revision和引用事实已存在 |
| claim_policy.update | policy revision | issuer，同时匹配body.expected_task_revision；仅offered，与claim串行；仅policy和应用事件推进，不改Task revision |
| session.begin/update/rotate/end | Session revision，begin=0 | 专题契约；update兼heartbeat，不追加核心Report |
| session.transfer | ExecutionBinding revision | 源/目标会话证明，同actor/项目、当前Assignment；不改核心Run/Task revision |
| wake.request | 当前Task revision | issuer、offered、目标profile匹配；产生Wake，不改Task revision |
| dispatch.reserve/authorize/report/resolve | Wake revision | 对应runner或admin；按唤起状态表 |

应用实体初始revision=1，成功变更加一；上限为安全整数，耗尽INVALID_STATE不回绕。project/handoff等不隐式合并字段；可省略的字段未传表示新版本不保留该资料。handoff源revision必须对应历史提交，submission/decision还须在该版本已存在且彼此归属一致，不从最新事实反向伪造历史依据。

## 5. 幂等与未知结果

核心键空间=(authority,actor.kind,actor.id,command.command_id)，完整封套与application按核心精确JSON比较；跨所有核心任务与操作。核心command改变返回core IDEMPOTENCY_CONFLICT；仅application改变返回binding IDEMPOTENCY_CONFLICT。

应用action键空间另为(authority,principal_type,principal_id,request_id)，actor ID包含kind，与runner严格分开。完整action封套参加比较，跨所有action。失败不产生成功回执；并发同键由事务唯一性串行化，成功事实/事件/回执共同提交。不得把在途标记当作成功回执。

回执命中重新认证并查当前历史read权，然后比较内容；不再要求write、角色、policy、session proof。runner可读自己dispatch回执。允许历史读取不恢复执行权，回执缺失不证明没有在途请求。Bearer/header证明不进等价内容、回执或事件。begin/rotate正文digest仅保存在私有等价记录，公共结果/事件剔除。

action成功={binding_version,request_id,replayed,result}；query={binding_version,result}，查询不写事件。回执和事件首版不清理；容量不足拒绝，不能删除旧键允许重执行。

超时、连接中断、5xx不能证明未提交。保留完整原请求，同键重试并查询当前事实，不创建重复任务或盲目释放。skill本地意图账本保存每步不同ID与原文，多个请求不是一笔事务。

## 6. 错误与读取

ErrorResponse={binding_version,error}；error为{scope:binding,code}或{scope:core,error:<upstream Error>}。HTTP精确映射见errors.json：400结构/版本/游标；401认证；403权限/证明；404不存在；409冲突/状态；507容量；503过载；500内部错误。传输层的405/413/415不伪装为业务错误。未知operation返回binding UNSUPPORTED_OPERATION；核心未知operation返回core UNSUPPORTED_OPERATION，不能统一吞为INVALID_MESSAGE。

service.info、task/workspace.snapshot、events.open/read、session.get、wake.get、profiles.list、dispatch.pending的结构见Schema。读取一致性、游标、容量和runner范围见专题契约。每次查询重验权限，不因拥有cursor而获得读权。

## 7. 证据边界

本轮校验结构、路由、操作覆盖、错误映射和规范一致性；Schema不验证数据库状态、角色、证明真实性、原子性或启动。行为向量只定义未来服务必须达到的结果。S2/S3/S4需分别完成事务、HTTP与launcher验证后才可宣称支持本binding。

本基线作为实施输入。字段或语义修改须记录并重新校验；issuer移交和外部进程停止仍无保证。上游反馈见[协议实践发现](protocol-findings.md)。
