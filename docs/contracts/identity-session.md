# 身份、实施会话与恢复契约

版本 ledger-http-draft-1，S1 实施基线。S3 已实现 HTTP 凭据适配和身份会话规则，实际验收范围见 [S3 验收记录](../validation/s3-agent-acceptance.md)。字段以[Schema](../../contract/v1/schema.json)为准；共同鉴权、回执次序和错误见[HTTP契约](../binding.md)。

## 主体目录

Participant={actor,display_name,state,revision,profile}，profile为provider/model/capabilities三个可选资料字段组成的对象，可传空对象。state=enabled/read_only。participant.create/update均需enabled admin+write，创建revision=0、首次实体revision=1，更新完整替换资料。ActorRef永久保留、不按名称合并、不复用；能力只是声明。

凭据带外配置，API无秘密签发。新凭据可以映射已有ActorRef，身份轮换不重写Task issuer。read_only禁新写但可持有效read凭据读取；凭据撤销直接401。停用不释放Assignment，不取消或完成任务。管理员本身不享有其他主体的issuer权。停用最后一个enabled管理员返回INVALID_STATE；遗失凭据的恢复由有权操作者带外配置，不能借此改Task角色。

## 会话证明

客户端为每个session生成独立32字节CSPRNG秘密，以无填充base64url保存于本机权限受限的意图记录。proof_digest为这32原始字节的SHA-256小写hex，begin仅提交digest。执行或会话变更发送X-Ledger-Session=<session_id>及X-Ledger-Session-Proof=<base64url secret>；服务器解码为恰好32字节，常量时间比较摘要。错误或缺失证明返回SESSION_PROOF_REQUIRED；与当前绑定不符返回SESSION_MISMATCH。

传输证明不参与请求等价，不进入日志、查询、错误或回执。begin/rotate正文的digest会用于私有成功请求等价比较，但不得出现在公开实体/事件。随机secret由客户端在第一次调用前持久保存，服务不需要向重试客户端再次披露secret。

## 操作和状态

| operation | body | revision与规则 |
|---|---|---|
| session.begin | session_id,project_id,goal,proof_digest；provider/host_ref/workspace_ref可选 | expected=0，ID未用过、项目存在；actor来自凭据。active、preparing、summary=goal、revision=1、proof_epoch=1 |
| session.update | session_id,phase,summary | Session CAS；同actor+当前proof、active；phase=preparing/implementing/testing/waiting，更新last_seen_at；兼heartbeat |
| session.rotate | session_id,proof_digest | Session CAS；同actor+旧proof、active；新digest不能是该session任何已用digest，否则INVALID_STATE；epoch/revision加一 |
| session.end | session_id,outcome,summary | Session CAS；同actor+proof、active；state=ended，outcome=done/failed/cancelled；保留phase、设置ended_at和last_seen_at |
| session.transfer | task,assignment_id,generation,target_session_id,reason | ExecutionBinding CAS；当前Assignment ID/generation，源与目标proof均须证明；同actor/同项目，目标active无占用 |

transfer源由X-Ledger-Session header标识，目标由body.target_session_id标识，另须X-Ledger-Target-Session-Proof。源可ended但proof仍有效；目标不可是源自身。其他会话动作header中的ID须与body一致。先查成功回执再查proof，因此rotate成功后可凭有效read身份重取原回执，但不能用旧proof做新写。

公开Session包含schema列出的状态、目标、来源、revision、proof_epoch、UTC毫秒created_at/last_seen_at；只有ended含outcome/ended_at。时间是服务观察，不用客户端时间判断执行有效性。每次update即使内容相同也是新的应用事实（同键重放例外），不会生成核心Report或改Task revision。

## 当前执行绑定

ExecutionBinding={task,assignment_id,generation,session_id,revision,state,run_id}；state=current/ended，run_id开始为null。Assignment内binding revision从1开始，不同Assignment不得混用revision。一个session最多占一个current Assignment，一个Assignment最多一个current session，事务内唯一约束覆盖并发claim/transfer。

claim原子创建binding；start关联Run并推进binding revision；transfer只换session并推进binding revision；Assignment因submit/fail/release/cancel结束时，原子标记binding ended、推进revision并释放会话占用。report不改binding。Session revision只由会话自身操作推进，避免将claim误作会话CAS。task_meta和session.get中的current_binding只显示current，已结束绑定通过应用事件保留。

transfer不改核心Task/Run/依据版本，两个证明与所有条件需在同一事务验证，源/目标rotate与transfer的竞争也按事务串行。目标proof只证明目标持有权，不为不同Actor授予承接权。同一主体作为issuer可以无session执行合法cancel/release/decide；这不是会话执行权绕过。

每个新的run.*命令须由当前绑定会话发出，proof须为最新且session active；ended返回SESSION_ENDED。claim也需要active空闲会话；session.end仍允许在有承接时成功，只会阻止后续执行写，不自动释放。之后明确transfer或由合法issuer release解锁。

## 两种接入和中断恢复

已有任务：begin→读取Task→claim并绑定→start→报告/交付。自发实施：begin→真实Agent create→claim→start。所有步骤各用不同command/request ID；组合意图账本保存完整请求和中间结果，部分完成不是失败回滚，不创建替代任务掩盖中断。

有secret的会话重连先核对当前事实，必要时rotate使旧proof失效；不得凭旧active回执直接开始外部工作。无secret不能强行恢复同一个session；由合法issuer release后新session claim，或有旧/新proof时显式transfer。会话heartbeat过期仅显示presence未知，不改Task/Assignment/Run。

Agent自发创建任务即是真实issuer，允许如实记录自验收但不称人工批准。要人最终验收就由人发起，或建立人发起的汇总任务；当前不提供管理员代验收或issuer转移。

## 保证限度

不同进程通常生成不同session秘密，因此重复唤起不能仅凭相同ActorRef借用Run。如果复制了同一Bearer和session secret，服务器无法分辨两个进程；不得把proof宣传成恶意同主体进程隔离。rotate/transfer可阻止旧证明的新服务写入，不能证明外部动作停止。

这些规则需要S2/S3的真实竞争、轮换、重放和恢复测试。静态消息校验不能证明凭据、CAS或绑定已经实现。
