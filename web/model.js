export const BINDING = 'ledger-http-draft-1';
export const PROTOCOL = 'task-core-draft-1';
export const STATES = {offered:'待领取',claimed:'已领取',active:'执行中',submitted:'待验收',completed:'已完成',rejected:'已拒绝',failed:'执行失败',cancelled:'已取消'};
export const OPERATIONS = {'task.create':'发布任务','task.claim':'领取任务','run.start':'开始执行','run.report':'进展报告','run.submit':'提交交付','task.decide':'验收决定','task.reopen':'重做任务','task.revise':'修订预期','assignment.release':'释放承接','run.fail':'执行失败','task.cancel':'取消任务'};
export const sameActor = (a,b) => !!a && !!b && a.kind===b.kind && a.id===b.id;
export const refKey = ref => JSON.stringify([ref.authority_id,ref.task_id]);
export const id = () => crypto.randomUUID();
export function normalize(bundle) {
  const task=bundle.snapshot.task, records=bundle.snapshot.records;
  const facts=type=>records.filter(x=>x.record_type===type).map(x=>x.record);
  const expectation=task.expectations.find(e=>e.version===task.expectation_version);
  const assignments=facts('Assignment'),runs=facts('Run');
  const assignment=assignments.find(a=>a.assignment_id===task.current_assignment_id);
  const run=runs.find(r=>r.run_id===task.current_run_id);
  const ext=expectation.extensions?.['io.taskledger.ui']||{};
  return {id:task.task_id,ref:{authority_id:task.authority_id,task_id:task.task_id},title:expectation.goal,goal:expectation.goal,expectedResult:expectation.expected_result,state:task.state,revision:task.revision,generation:task.generation,issuer:task.issuer,assignee:assignment?.assignee,relations:task.relations.map(r=>({...r,task:r.target})),branchKind:ext.kind||null,groupId:ext.group_id,task,expectation,assignment,run,assignments,runs,reports:facts('Report'),submissions:facts('Submission'),decisions:facts('Decision'),application:bundle.application};
}
export function allowed(task,identity,participants=[]) {
  const participant=participants.find(p=>sameActor(p.actor,identity?.actor));
  const write=!!identity?.write && participant?.state==='enabled';
  const issuer=write&&sameActor(task.issuer,identity.actor),assignee=write&&sameActor(task.assignee,identity.actor);
  const terminal=['completed','cancelled'].includes(task.state);
  const policy=task.application.claim_policy;
  return {create:write,claim:write&&task.state==='offered'&&(policy.mode==='workspace'||sameActor(policy.target,identity.actor)),start:assignee&&task.state==='claimed',report:assignee&&task.state==='active',submit:assignee&&task.state==='active',fail:assignee&&task.state==='active',release:(issuer||assignee)&&['claimed','active'].includes(task.state),decide:issuer&&task.state==='submitted',reopen:issuer&&['failed','rejected'].includes(task.state),cancel:issuer&&!terminal,revise:issuer&&!terminal,policy:issuer&&task.state==='offered'};
}
export function command(operation,task,body,application={},commandId=id()) {
  return {binding_version:BINDING,command:{protocol_version:PROTOCOL,command_id:commandId,task:task.ref,expected_revision:task.revision,operation,body},application};
}
export function createPlan({authority,projectId,goal,expectedResult,target=null,parent=null,kind='child',count=1}) {
  if(!goal.trim()||!expectedResult.trim())throw new Error('请填写目标和预期结果');
  if(!Number.isInteger(count)||count<1||count>6)throw new Error('每次创建 1–6 个独立尝试');
  const group=id();
  return Array.from({length:count},(_,i)=>{
    const task={ref:{authority_id:authority,task_id:id()},revision:0};
    const body={goal:count>1?`${goal.trim()} · 尝试 ${i+1}`:goal.trim(),expected_result:expectedResult.trim()};
    if(parent)body.relations=[{kind:kind==='child'?'parent':'related',target:parent.ref}];
    if(parent||count>1)body.extensions={'io.taskledger.ui':{kind:count>1?'variant':kind,group_id:group,variant_index:i+1}};
    return command('task.create',task,body,{project_id:projectId,claim_policy:target?{mode:'target',target}:{mode:'workspace'}});
  });
}
export function runBody(task) {
  if(!task.assignment)throw new Error('任务没有当前承接，请刷新后再操作');
  const body={assignment_id:task.assignment.assignment_id,generation:task.assignment.generation};
  if(task.run)body.run_id=task.run.run_id;
  return body;
}
export function safeLink(uri) {
  try {const url=new URL(uri);return ['http:','https:'].includes(url.protocol)?url.href:null;}catch{return null;}
}
