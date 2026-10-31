# 读取接续与可选唤起契约

版本 ledger-http-draft-1，S1 实施基线。S3 已实现快照、签名 cursor 和七个公开查询；唤起与 runner 调度仍待 S4，实际证据见 [S3 验收记录](../validation/s3-agent-acceptance.md)。精确字段见[Schema](../../contract/v1/schema.json)；共同封套/权限/错误见[HTTP契约](../binding.md)。以下不是运行验证结果。

## 查询与公开对象

| operation | body | result |
|---|---|---|
| service.info | {} | authority、binding/protocol/upstream commit、11操作、固定容量 |
| task.snapshot | {task} | {bundle:{snapshot,application},watermark,event_cursor} |
| workspace.snapshot | {} | {tasks:[TaskBundle],projects,participants,handoffs,sessions,wakes,profiles,watermark,event_cursor} |
| events.open | {after?,task?} | {cursor,watermark} |
| events.read | {cursor,limit:1..200} | {events,next_cursor,follow_cursor,done} |
| session.get | {session_id} | {session,current_binding} |
| profiles.list | {target?} | {profiles}；target限agent |
| wake.get | {wake_id} | Wake对象本身 |
| dispatch.pending | {limit:1..200} | {wakes}；仅runner自己的queued候选 |

Actor read可看全工作空间公开事实，包括目录、会话摘要和启动结果；不包含secret/digest、executable、argv、环境或秘密引用。runner只能看自己的pending与wake.get及dispatch回执，不读Task快照或借此成为Agent。候选按持久创建顺序排列，不保证reserve成功；消费后可继续取队列。

LaunchProfile公开字段={profile_id,revision,catalog_revision,target,runner_id,label,enabled}。revision是不可变配置版本；catalog_revision是这个(profile_id,revision)的公开观察版本，初次导入为1，启停时增加。带外配置导入/启停必须经过宿主事务，产生entity_type=profile的公开应用事件，entity_id对应完整(profile_id,revision)，事件revision等于catalog_revision。没有凭据时由可信配置操作者映射的admin actor记录；不能以匿名JSON配置自报actor。私有命令不进入日志。快照及其后事件因而能恢复配置元数据，不能仅更改内存配置绕过日志。每次新操作按当前启停判断，不静默替换请求引用的版本。

## 快照与日志

snapshot采用同一一致性读视图，返回对象及完整事务边界水位W；event_cursor从W之后开始。单task游标固定TaskRef范围，workspace游标为全authority。workspace最多1000任务，集合中各类对象最多1000，整个响应最多16MiB；任一超限整个请求CAPACITY_LIMIT，不截断或返回部分水位。单task也受16MiB限制。首版不提供分页快照；任务发现先使用workspace snapshot筛选，超限是明确容量边界，后续再演进分页接口。

LogEntry={position,recorded_at,source,event}，position为宿主不透明位置，recorded_at为UTC毫秒时间。source=core保持上游Event原样；application事件带event_id、operation、entity_type/id、revision、actor或runner_id、公开after-image、removed（本版恒false）。同一authority内两种来源的event_id也必须全局唯一。任务相关应用事件还带TaskRef，policy/binding实体键包含完整TaskRef（binding还含assignment_id），不能将不同任务的同名Assignment合并。日志只能证明单authority提交顺序，不能按时间或核心event_id推导全局顺序。

每个成功写事务可追加多条日志，Task核心事件、binding和wake等应用事件同事务提交。水位不能位于事务中间。事件分页可在事务内部切页，position保留精确接续位置；客户端须按各对象revision推进，不能把分页中间状态宣称为一个完整事务快照。快照水位仅在完整事务后。

## 水位与cursor

token由服务持久验证密钥签名或由持久cursor表实现，客户端不能拼接位置。watermark绑定authority、日志incarnation、范围（全authority或指定TaskRef），不绑定actor；有当前read权者可复用。cursor另绑定ActorRef，换凭据但身份相同可继续，不同actor使用返回FORBIDDEN。scope不匹配或格式错误INVALID_CURSOR；旧incarnation返回CURSOR_RESET_REQUIRED。

events.open捕获完整提交上界H，将(after,H]固定成批次。after缺省从日志起点；提供after须与task过滤范围一致。返回watermark=H，只有消费完整批次后才能将其记为已处理水位。snapshot的event_cursor为follow cursor，首次read捕获新的H。

固定批次read按位置返回最多limit项；done=false时next_cursor非空且follow_cursor=null；done=true时next_cursor=null且follow_cursor非空，从H之后继续。无匹配事件也要推进范围，允许空且done=true。相同固定cursor与limit重读返回相同前缀。

读取follow cursor会捕获新上界H2，再按固定批次规则分页；同一follow重试可能包含更大的前缀，不是命令回执。客户端按authority+event_id去重，按Task或应用对象revision防止倒退，只在处理结果后保存next/follow。task过滤包含其核心事件和带该TaskRef的policy/binding/wake事件，不包含无Task归属的participant/project/session更新；需要全视图的消费者用workspace cursor。

正常重启保留日志、密钥和incarnation。旧备份恢复或重建历史必须更换incarnation；旧cursor明确要求重取快照。首版不自动TTL/清理事件、回执、cursor，不以丢历史恢复容量；容量满明确拒绝新写。查询每次重验读权，token不授予权限。

## 创建与唤起准入

管理者带外配置LaunchProfile，网络API只读元数据，不上传可执行命令。目标需enabled Agent，Task为offered且ClaimPolicy明确target，profile与目标和runner匹配。create带wake选项时Task/项目/policy/Wake/事件/回执同事务提交；无有效配置则整笔PROFILE_UNAVAILABLE。后续wake.request仅issuer，expected_revision检查Task，不改Task revision。

同一(TaskRef,target)存在queued/reserved/authorized/unknown时WAKE_IN_PROGRESS；started后只可通过显式新意图再次请求，不自动重启。wake_id、activation_id服务生成，公共Wake字段精确定义于Schema，初始queued、revision=1、activation_id和evidence=null。

## 动作与状态

| operation | body | 权限/转换 |
|---|---|---|
| wake.request | task,profile_id,profile_revision | issuer；校验后queued |
| dispatch.reserve | wake_id | matching runner，queued→reserved，生成唯一activation_id；已失效则skipped，不生成activation |
| dispatch.authorize | wake_id,activation_id | 同runner，reserved→authorized；失效则skipped |
| dispatch.report | wake_id,activation_id,outcome,evidence | 同runner；outcome=started/not_started/unknown |
| dispatch.resolve | wake_id,outcome,evidence | enabled admin；outcome=started/not_started，有证据解决未决 |

除了wake.request检查Task revision，其余CAS Wake revision。reserved只可report not_started→failed；authorized可report→started/failed/unknown；unknown可同runner补证started/not_started。resolve允许reserved/authorized/unknown→started/failed。started/failed/skipped终态，不能重写或新建activation；同键回执仍先于状态守卫。

reserve/authorize重验Task、target、policy和profile，失效作为成功skipped事实及回执，不伪称启动。每runner最多一个reserved/authorized/unknown，超限OVERLOADED503；queued不占名额。没有预约到期、自动重派、自动更换runner。任务仍可主动领取，唤起未决不阻断Task的合法操作。

report/resolve的evidence必须是公开可分享摘要，不含进程环境或秘密。operator声称not_started之前必须隔离旧runner并核对启动日志；超时、无session或暂未查到进程不是未启动证明。迟到report与resolve同revision竞争，失败者重读，不再次spawn或覆盖历史。

## 外部启动边界与本地日志

authorize是服务最后一次原子校验的点，其后取消、停用或撤销无法与外部spawn原子化。不能承诺取消后绝不出现进程；目标后续claim/start仍重新验证资格。authorized在runner无回应时本身就是待核对，不随时间恢复可重派。

一个runner_id绑定单个受控安装目录和持久日志，launcher必须取得目录独占进程锁，禁止跨主机复制同runner身份配独立空日志。一个activation最多调用一次spawn：

1. 按当前wake核对reserve/authorize，不能只拿历史授权回执重复启动。
2. 本地日志先持久记录spawn_attempted，完成后才调用exec。
3. 若已有spawn_attempted，禁止再次spawn，先核对并报告unknown或确切结果。
4. 启动成功持久保存可核对事实后report；崩溃时可能保守地保留“有attempt记录但未真正启动”的unknown。

这不提供外部副作用exactly-once。使用固定executable与独立argv，不拼接任务正文到shell；目标正文和产物从服务读取。凭据由受控执行端解析，秘密不进公共事件。started只代表进程启动，不代表会话已建立、任务已领取或已执行。

## 工程出口

S2/S3/S4验证快照边界、固定与follow分页、跨身份cursor、创建回滚、reserve竞争、authorize后撤销、持久attempt前后崩溃、未知结果和迟到报告。R14/R15的“重新核对”保证只到authorize事务；之后仍可能启动，必须如实观察。静态规格不证明其中任何运行保证。
