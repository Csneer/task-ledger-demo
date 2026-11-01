import {LedgerAPI} from './api.js';
import {STATES,OPERATIONS,normalize,allowed,sameActor,createPlan,runBody,safeLink,id} from './model.js';
import {TaskGraph} from './graph.js';
import {HistoryFeed} from './history.js';
import {groupTasks,groupNodes,focusNeighborhood} from './journeys.js';

const $=selector=>document.querySelector(selector);
const escape=value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const api=new LedgerAPI();
const state={connected:false,tasks:[],workspace:null,project:'',filter:'',selected:null,events:[],cursor:null,seen:new Set(),polling:false,epoch:0,dialog:null,view:'overview',chainId:null,focusId:null,groups:[],historyEvents:[],activity:'all',overviewPage:0};
const graph=new TaskGraph($('#graph'),{onSelect:taskId=>{const group=state.groups.find(g=>g.id===taskId);if(group)selectView('chain',group.id,group.anchorId);else showTask(taskId);}});
let taskHistory=null,taskHistoryId=null;
const history=new HistoryFeed((operation,body)=>api.query(operation,body),(events,meta)=>{state.historyEvents=events;state.historyMeta=meta;if(state.view!=='task')graph.setHistory(events,meta);if(state.connected)draw();});
const dialog=$('#dialog'),content=$('#dialog-content');
let focusReturn,toastTimer,snapshotPromise,working=false;

function toast(message){$('#toast').textContent=message;$('#toast').hidden=false;clearTimeout(toastTimer);toastTimer=setTimeout(()=>$('#toast').hidden=true,4500);}
function notice(message=''){$('#notice').textContent=message;$('#notice').hidden=!message;}
const actorName=actor=>state.workspace?.participants.find(p=>sameActor(p.actor,actor))?.display_name||actor?.id||'尚未领取';
const canWrite=()=>api.identity?.write&&state.workspace?.participants.some(p=>sameActor(p.actor,api.identity.actor)&&p.state==='enabled');
const canAdmin=()=>canWrite()&&api.identity?.admin;
function overviewCapacity(){const canvas=$('#graph'),columns=canvas.clientWidth>=1000?4:canvas.clientWidth>=650?3:2;return Math.min(innerWidth<700?4:12,columns*Math.max(1,Math.floor((canvas.clientHeight-85)/165)));}
function errorText(error){const messages={STALE_REVISION:'任务已被其他客户端更新，请刷新后重新决定。',ROLE_MISMATCH:'当前身份没有这项任务操作权。',FORBIDDEN:'当前凭据没有操作权限。',UNAUTHENTICATED:'凭据无效或已撤销，请重新连接。',INVALID_STATE:'当前任务状态不允许此操作。',SESSION_MISMATCH:'执行已由其他会话承接，请重新读取当前事实。',CAPACITY_LIMIT:'工作空间达到后端容量上限；未返回完整视图。',SESSION_ENDED:'执行会话已经结束。'};return (error.unknown?'结果尚未确认。请使用“核对未确认操作”重试原请求，勿重复发布。\n':'')+(messages[error.code]||error.message);}
function closeDialog(){dialog.close();state.dialog=null;graph.setPlaybackSuspended(false);focusReturn?.focus?.();}
function openDialog(title,html,tag='TASK LEDGER',kind='form'){
  if(!dialog.open)focusReturn=document.activeElement;
  state.dialog=kind;
  graph.setPlaybackSuspended(true);
  content.innerHTML=`<div class="dialog-head"><span class="dialog-id">${escape(tag)}</span><button class="close" type="button" aria-label="关闭弹窗">×</button></div><h2>${escape(title)}</h2>${html}`;
  content.querySelector('.close').onclick=closeDialog;
  if(!dialog.open)dialog.showModal();
  (content.querySelector('input,textarea,select')||content.querySelector('.close')).focus();
}
function input(name,label,value='',{type='text',required=true,placeholder=''}={}) {return `<label for="field-${name}">${escape(label)}</label><input id="field-${name}" name="${name}" type="${type}" value="${escape(value)}" ${required?'required':''} placeholder="${escape(placeholder)}">`;}
function area(name,label,value=''){return `<label for="field-${name}">${escape(label)}</label><textarea id="field-${name}" name="${name}" required>${escape(value)}</textarea>`;}
function form(body,label='确认'){return `<form>${body}<p class="form-error" role="alert"></p><button class="primary submit" type="submit">${escape(label)}</button></form>`;}
async function exclusive(callback){
  if(working||api.busy)throw new Error('操作正在提交，请等待回执后再继续。');
  working=true;
  try{return await callback();}finally{working=false;pendingControls();}
}
function bindForm(callback){const f=content.querySelector('form');f.onsubmit=async event=>{event.preventDefault();if(!f.reportValidity())return;const button=f.querySelector('[type=submit]');button.disabled=true;f.querySelector('.form-error').textContent='';try{await exclusive(()=>callback(Object.fromEntries(new FormData(f))));}catch(e){if(f.isConnected)f.querySelector('.form-error').textContent=errorText(e);else notice(errorText(e));}finally{if(button.isConnected)button.disabled=false;pendingControls();}};}
function pendingControls(){const connected=state.connected;$('#pending').hidden=!connected||!api.pending();$('#batch-resume').hidden=!connected||!api.read('batch');$('#create').disabled=!connected||!canWrite()||!!api.pending();$('#empty-create').disabled=!connected||!canWrite();}

function draw(events=[]){
  const projects=state.workspace?.projects||[];
  const project=projects.find(p=>p.project_id===state.project);
  const projectTasks=state.tasks.filter(t=>!state.project||t.application.project_id===state.project);
  state.groups=groupTasks(projectTasks,state.historyEvents);
  let group=state.groups.find(g=>g.id===state.chainId);
  if(state.view!=='overview'&&!group){state.view='overview';state.chainId=null;}
  const matches=t=>!state.filter||(state.filter==='attention'?['offered','submitted','rejected','failed'].includes(t.state):t.state===state.filter);
  const groups=state.groups.filter(g=>(state.activity==='all'||(state.activity==='worked'?g.hasExecution:!g.hasExecution))&&g.tasks.some(matches));
  const pageSize=overviewCapacity();
  if(state.overviewPageSize!==pageSize){state.overviewPage=0;state.overviewPageSize=pageSize;}
  const pages=Math.max(1,Math.ceil(groups.length/pageSize));state.overviewPage=Math.min(state.overviewPage,pages-1);
  let visible,description,partial=false;
  if(state.view==='overview'){
    visible=groupNodes(groups.slice(state.overviewPage*pageSize,(state.overviewPage+1)*pageSize));
    description=`${groups.length} 条任务链 · ${projectTasks.length} 项任务 · ${projectTasks.filter(t=>t.state==='completed').length} 已完成${pages>1?` · 本页 ${visible.length} 条`:''} · 点击展开`;
  }else{
    const candidates=state.view==='task'?group.tasks.filter(t=>t.id===state.focusId):group.tasks.filter(matches);
    const window=state.filter?{tasks:candidates.slice(0,24),hiddenCount:Math.max(0,candidates.length-24)}:focusNeighborhood(candidates,state.focusId,24);visible=window.tasks;
    description=state.view==='task'?'单任务过程 · 回放与当前状态分别呈现':group.summaryText;
    partial=state.view==='chain'&&(!!state.filter||window.hiddenCount>0);
    if(partial)description+=` · 当前${state.filter?'筛选':'邻域'} ${visible.length}/${group.tasks.length} 项 · 回放仅含当前可见任务`;
    if(window.hiddenCount)description+=' · 选择任务可定位其余项';
    if(!group.hasExecution)description+=' · 尚无承接记录';
  }
  $('#project-title').textContent=state.view==='overview'?(project?.title||'工作在这里，继续流动。'):state.view==='task'?(taskById(state.focusId)?.title||group.title):group.title;
  $('#workspace').textContent='工作空间 ⌄';
  $('#project-caption').textContent=state.connected?description:'人与 Agent 的协作，在这里留下轨迹。';
  const select=$('#project-select'),selected=state.project;
  select.innerHTML='<option value="">全部项目</option>'+projects.map(p=>`<option value="${escape(p.project_id)}">${escape(p.title)}</option>`).join('');select.value=selected;
  graph.setData(visible.map(t=>({...t,displayName:actorName(t.assignee)})),{selectedId:state.selected,events,participants:state.workspace?.participants||[],scopeLabel:state.view==='overview'?'任务链总览':state.view==='task'?'单任务过程':partial?'任务链局部过程':'任务链过程'});
  $('#back-overview').hidden=state.view==='overview';$('#back-chain').hidden=state.view!=='task';
  $('#choose-focus').hidden=state.view==='overview';$('#activity-select').hidden=state.view!=='overview';
  $('#state-select').disabled=state.view==='task';
  $('#overview-pages').hidden=state.view!=='overview'||pages<=1;$('#overview-page').textContent=`${state.overviewPage+1} / ${pages}`;
  $('#overview-prev').disabled=state.overviewPage===0;$('#overview-next').disabled=state.overviewPage===pages-1;
  $('#view-context').textContent=state.view==='overview'?'任务链总览':state.view==='task'?'单任务过程':'任务链 · 按显式关系展开';
  $('#scope-tools').hidden=!state.connected;
  $('#empty').hidden=visible.length>0||!state.connected;
  $('#empty h2').textContent=projectTasks.length?'当前筛选没有匹配任务':'一个目标，一条新的轨迹。';
  $('#empty p').textContent=projectTasks.length?'调整状态或实施范围，查看其他任务。':'发布第一项任务，再由人与 Agent 接续完成。';
  $('#empty-reset').hidden=!projectTasks.length;
  pendingControls();
}
function selectView(view,chainId=null,taskId=null){
  if(taskHistory){taskHistory.reset();taskHistory=null;taskHistoryId=null;}
  state.view=view;state.chainId=chainId;state.focusId=taskId;state.selected=taskId;state.filter='';$('#state-select').value='';
  graph.setHistory(state.historyEvents,state.historyMeta);
  draw();graph.fit();if(view!=='overview'&&graph.scale<.65&&taskId)graph.focus(taskId);
  if(view==='task'){
    const task=taskById(taskId);if(!task)return;
    const epoch=state.epoch;taskHistoryId=taskId;
    taskHistory=new HistoryFeed((operation,body)=>api.query(operation,operation==='events.open'?{task:task.ref}:body),(events,meta)=>{if(state.epoch===epoch&&state.view==='task'&&state.focusId===taskId)graph.setHistory(events,meta);},{maxEvents:5000});
    void taskHistory.read();
  }
}
function setProject(projectId){state.project=projectId;state.overviewPage=0;selectView('overview');}
function taskView(task,view){
  state.project=task.application.project_id;
  const groups=groupTasks(state.tasks.filter(t=>t.application.project_id===state.project),state.historyEvents);
  const group=groups.find(g=>g.memberIds.includes(task.id));if(!group)return;
  closeDialog();selectView(view,group.id,task.id);
}
function chainDialog(){
  openDialog('选择一条任务链','<input id="chain-search" aria-label="搜索任务链" placeholder="目标、任务名称或 ID…"><div id="chain-results"></div>','TASK CHAINS','chains');
  const input=content.querySelector('input'),list=content.querySelector('#chain-results');
  const render=()=>{const term=input.value.toLocaleLowerCase();const found=state.groups.filter(g=>g.tasks.some(t=>[t.id,t.title].join(' ').toLocaleLowerCase().includes(term)));list.innerHTML=found.slice(0,50).map(g=>`<button class="menu-row chain-link" data-id="${escape(g.id)}"><span>${escape(g.title)}<small class="menu-description">${escape(g.summaryText)}</small></span><small>${g.hasExecution?'有实施记录':'尚未承接'}</small></button>`).join('')+(found.length>50?'<p class="subtle">显示前 50 条，请搜索缩小范围。</p>':'');if(!found.length)list.textContent='没有匹配的任务链';list.querySelectorAll('button').forEach(b=>b.onclick=()=>{const g=state.groups.find(g=>g.id===b.dataset.id);closeDialog();selectView('chain',g.id,g.anchorId);});};input.oninput=render;render();
}
function focusDialog(){
  const group=state.groups.find(g=>g.id===state.chainId);if(!group)return;
  openDialog('定位链中的任务','<input id="focus-search" aria-label="搜索链中任务" placeholder="任务名称、ID 或状态…"><div id="focus-results"></div>',group.title,'focus');
  const input=content.querySelector('input'),list=content.querySelector('#focus-results');
  const render=()=>{const term=input.value.toLocaleLowerCase();const found=group.tasks.filter(t=>[t.id,t.title,STATES[t.state]].join(' ').toLocaleLowerCase().includes(term));list.innerHTML=found.slice(0,50).map(t=>`<button class="menu-row" data-id="${escape(t.id)}"><span>${escape(t.title)}</span><small>${STATES[t.state]}</small></button>`).join('')+(found.length>50?'<p class="subtle">显示前 50 项，请搜索定位其余任务。</p>':'');list.querySelectorAll('button').forEach(b=>b.onclick=()=>{closeDialog();selectView('chain',group.id,b.dataset.id);graph.focus(b.dataset.id);});};input.oninput=render;render();
}
async function refresh(events=[]){
  if(!state.connected)return;
  if(snapshotPromise)await snapshotPromise.catch(()=>{});
  const epoch=state.epoch;
  const request=api.query('workspace.snapshot');snapshotPromise=request;
  try{
    const ws=await request;if(epoch!==state.epoch)return;
    state.workspace=ws;state.tasks=ws.tasks.map(normalize);state.cursor=ws.event_cursor;
    $('#connection').className='connection online';$('#connection span').textContent=actorName(api.identity.actor);
    notice();draw(events);
    if(state.dialog==='detail'&&state.selected)showTask(state.selected,true);
  }finally{if(snapshotPromise===request)snapshotPromise=null;}
}
async function connect(token){
  if(api.busy)throw new Error('操作正在提交，请等待回执后再切换身份。');
  state.epoch++;state.connected=false;state.cursor=null;state.view='overview';state.chainId=null;state.focusId=null;state.project='';state.filter='';state.activity='all';state.overviewPage=0;$('#state-select').value='';$('#activity-select').value='all';
  history.reset();
  if(taskHistory){taskHistory.reset();taskHistory=null;taskHistoryId=null;}
  await api.connect(token);state.connected=true;state.events=[];state.seen.clear();
  await refresh();$('#event-label').textContent='已连接 · 等待新的任务事件';
  void history.read();
}
function connectionDialog(){
  if(working||api.busy){toast('操作正在提交，请等待回执后再切换身份。');return;}
  openDialog('连接你的任务空间',form(`<p class="connection-description">连接当前 Go 服务。凭据决定真实身份，人与 Agent 使用相同的任务协议。</p>${input('token','访问凭据','',{type:'password',placeholder:'粘贴管理员提供的凭据'})}<p class="field-help">仅保留在当前浏览器标签页；不写入 URL、任务正文或公开日志。</p>`,'连接'),location.origin,'connect');
  bindForm(async data=>{await connect(data.token);closeDialog();toast('已连接真实任务服务');});
}
async function poll(){
  if(!state.connected||state.polling||document.hidden||!state.cursor)return;
  state.polling=true;const epoch=state.epoch;let cursor=state.cursor,entries=[];
  try{
    for(let page=0;page<25;page++){
      const result=await api.query('events.read',{cursor,limit:200});if(epoch!==state.epoch)return;
      entries.push(...result.events);cursor=result.done?result.follow_cursor:result.next_cursor;
      if(result.done)break;
      if(page===24){await refresh();$('#event-label').textContent='更新较多，已重新读取完整工作空间';return;}
    }
    const fresh=entries.filter(e=>!state.seen.has(e.event.event_id));
    if(fresh.length){
      const core=fresh.filter(e=>e.source==='core');
      const last=core.at(-1)||fresh.at(-1);
      $('#event-label').textContent=`${OPERATIONS[last.event.operation]||last.event.operation} · ${last.event.task?.task_id?.slice(0,12)||actorName(last.event.actor)}${fresh.length>1?` · ${fresh.length} 条更新`:''}`;
      await refresh(core.slice(-20).map(e=>({event_id:e.event.event_id,operation:e.event.operation,task_id:e.event.task.task_id})));
      fresh.forEach(e=>state.seen.add(e.event.event_id));
      if(state.seen.size>5000)state.seen=new Set([...state.seen].slice(-2500));
      state.events=[...state.events,...fresh].slice(-200);
    }else{state.cursor=cursor;$('#connection').className='connection online';}
  }catch(e){$('#connection').className='connection offline';if(epoch===state.epoch){if(e.code==='CURSOR_RESET_REQUIRED'){await refresh().catch(error=>notice(errorText(error)));}else notice('连接暂时中断，保留最后确认的事实。'+errorText(e));}}
  finally{state.polling=false;}
}
async function afterWrite(message){const events=api.recentEvents.splice(0).filter(e=>!state.seen.has(e.event_id));await refresh(events.map(e=>({event_id:e.event_id,operation:e.operation,task_id:e.task.task_id})));events.forEach(e=>state.seen.add(e.event_id));$('#event-label').textContent=message;toast(message);pendingControls();}
function taskById(taskId){return state.tasks.find(t=>t.id===taskId);}
function historyMarkup(task){
  const submissions=task.submissions.map(s=>{const decision=task.decisions.find(d=>d.submission_id===s.submission_id);const assignment=task.assignments.find(a=>a.assignment_id===s.assignment_id);return `<article class="history-row" data-submission="${escape(s.submission_id)}"><div class="history-top"><span>交付 · 尝试 ${assignment?.generation||'—'} · ${escape(actorName(s.submitted_by))}</span><span>依据 v${s.basis_version}</span></div><div class="prose">${escape(s.summary)}</div>${s.artifacts.length?'<ul class="artifacts">'+s.artifacts.map(a=>{const url=safeLink(a.uri);return `<li>${url?`<a href="${escape(url)}" target="_blank" rel="noopener noreferrer">${escape(a.uri)}</a>`:escape(a.uri)}</li>`}).join('')+'</ul>':''}${decision?`<div class="history-top"><span>${decision.outcome==='accepted'?'已接受':'已拒绝'} · ${escape(actorName(decision.decided_by))}</span><span>验收 v${decision.evaluated_version}</span></div><div class="prose">${escape(decision.reason)}</div>`:`<p class="subtle">${task.task.pending_submission_id===s.submission_id?'等待发起者验收':'未产生验收决定'}</p>`}</article>`;}).join('');
  const reports=task.reports.slice(-10).reverse().map(r=>`<article class="history-row"><div class="history-top">进展 · ${escape(actorName(r.reported_by))}</div><div class="prose">${escape(r.message)}</div></article>`).join('');
  return `<section class="detail-section"><h3>交付与验收</h3>${submissions||'<p class="subtle">尚未正式交付。</p>'}</section><section class="detail-section"><h3>最近进展</h3>${reports||'<p class="subtle">等待执行者的报告。</p>'}</section>`;
}
function showTask(taskId,refreshing=false){
  const t=taskById(taskId);if(!t)return;state.selected=taskId;if(!refreshing)graph.focus(taskId);
  const scroll=refreshing?dialog.scrollTop:0,focusedAction=refreshing?document.activeElement?.dataset?.action:null;
  const a=allowed(t,api.identity,state.workspace.participants),target=t.application.claim_policy.target;
  const buttons=[['child','＋ 子任务',a.create],['variant','✧ 多开尝试',a.create],['retry','↻ 新建重做',a.create&&['completed','cancelled'].includes(t.state)],['claim','领取任务',a.claim],['start','开始执行',a.start],['report','记录进展',a.report],['submit','提交交付',a.submit],['accept','接受交付',a.decide],['reject','拒绝交付',a.decide],['reopen','↻ 重新开放',a.reopen],['policy','指定 Agent',a.policy],['revise','修订预期',a.revise],['release','释放承接',a.release],['fail','报告失败',a.fail],['cancel','取消任务',a.cancel],['chain','查看任务链',true],['replay','回看流转',true],['history','完整时间线',true]];
  const relations=state.tasks.filter(other=>other.relations.some(r=>r.target.authority_id===t.ref.authority_id&&r.target.task_id===t.id));
  const outgoing=t.relations.map(r=>{const linked=state.tasks.find(x=>x.id===r.target.task_id&&x.ref.authority_id===r.target.authority_id);const label={parent:'父任务',related:'关联任务',depends_on:'依赖任务',external:'外部任务'}[r.kind]||r.kind;return linked?`<button class="menu-row related-link" data-id="${escape(linked.id)}"><span>${label} · ${escape(linked.title)}</span><small>${STATES[linked.state]}</small></button>`:`<p class="subtle">${label} · ${escape(r.target.authority_id)} / ${escape(r.target.task_id)}（不在本工作空间快照内）</p>`}).join('');
  openDialog(t.title,`<div class="detail-state" data-state="${t.state}">${STATES[t.state]} <span class="quiet">· 版本 ${t.revision} · 尝试 ${t.generation}</span></div><div class="facts-grid"><div><div class="fact-label">发起者 / 验收者</div><div class="fact-value">${escape(actorName(t.issuer))}</div></div><div><div class="fact-label">${t.assignee?'当前承接者':target?'指定，尚未承接':'当前承接者'}</div><div class="fact-value">${escape(actorName(t.assignee||target))}</div></div></div><section class="detail-section"><h3>预期结果 · v${t.task.expectation_version}${t.run?` / 当前执行依据 v${t.run.basis_version}`:''}</h3><div class="prose">${escape(t.expectedResult)}</div></section>${historyMarkup(t)}${relations.length||outgoing?`<section class="detail-section"><h3>派生与关联任务</h3>${outgoing}${relations.map(r=>`<button class="menu-row related-link" data-id="${escape(r.id)}"><span>${escape(r.title)}</span><small>${STATES[r.state]}</small></button>`).join('')}</section>`:''}<div class="task-actions">${buttons.filter(b=>b[2]).map(([key,label])=>`<button data-action="${key}" class="${['cancel','fail','reject'].includes(key)?'danger':''}">${label}</button>`).join('')}</div><p class="subtle">操作以当前身份与服务校验为准；子任务的结果不会自动改变父任务。</p>`,t.id,'detail');
  content.querySelectorAll('[data-action]').forEach(b=>b.onclick=()=>taskAction(b.dataset.action,t));
  content.querySelectorAll('.related-link').forEach(b=>b.onclick=()=>showTask(b.dataset.id));
  dialog.scrollTop=scroll;
  if(focusedAction)content.querySelector(`[data-action="${focusedAction}"]`)?.focus({preventScroll:true});
}
function targetSelect(target){const agents=state.workspace.participants.filter(p=>p.actor.kind==='agent'&&p.state==='enabled');return `<label for="target-agent">领取对象</label><select id="target-agent" name="target"><option value="">开放领取（人或 Agent）</option>${agents.map(p=>`<option value="${escape(p.actor.id)}" ${target?.id===p.actor.id?'selected':''}>${escape(p.display_name)}</option>`).join('')}</select><p class="field-help">指定只限制领取资格。Agent 由当前会话启动后自行领取，Web 不自动唤起。</p>`;}
function createDialog(parent=null,kind='child'){
  if(!canWrite()){toast('请使用有写权限的身份连接');return;}
  if(!state.workspace.projects.length){projectDialog();return;}
  const title=parent?(kind==='variant'?'多开独立尝试':kind==='retry'?'创建新一轮重做':'派发子任务'):'发布一个目标';
  const projectOptions=state.workspace.projects.map(p=>`<option value="${escape(p.project_id)}" ${(parent?.application.project_id||state.project)===p.project_id?'selected':''}>${escape(p.title)}</option>`).join('');
  openDialog(title,form(`${parent?`<p class="subtle">关联：${escape(parent.title)}。每项新任务由当前身份发起、独立交付与验收。</p>`:''}${input('goal','任务目标',parent&&kind!=='child'?parent.goal:'')}${area('expected','预期结果',parent&&kind!=='child'?parent.expectedResult:'')}<div class="form-grid"><div><label for="task-project">项目</label><select id="task-project" name="project">${projectOptions}</select></div>${kind==='variant'?`<div>${input('count','并行尝试数','3',{type:'number'})}</div>`:''}</div>${targetSelect(null)}${kind==='variant'?'<p class="field-help">每份尝试有独立任务 ID；逐项发布，部分成功会保留。不会在同一个任务上并行领取。</p>':''}`,'发布任务'),parent?.id||'NEW TASK','create');
  bindForm(async data=>{
    if(api.read('batch'))throw new Error('已有尚未发布完的尝试，请先使用底部“继续发布尝试”。');
    const plan=createPlan({authority:api.info.authority_id,projectId:data.project,goal:data.goal,expectedResult:data.expected,target:data.target?{kind:'agent',id:data.target}:null,parent,kind,count:kind==='variant'?Number(data.count):1});
    api.save('batch',{plan,index:0});await resumeBatch();
  });
}
async function resumeBatch(){
  const batch=api.read('batch');if(!batch)return;
  for(;batch.index<batch.plan.length;){
    await api.write('/v1/commands',batch.plan[batch.index]);batch.index++;api.save('batch',batch);
    $('#event-label').textContent=`已发布 ${batch.index} / ${batch.plan.length} 个任务`;
  }
  api.save('batch',null);if(dialog.open)closeDialog();await afterWrite(`已发布 ${batch.plan.length} 项独立任务`);
}
function batchDialog(){const batch=api.read('batch');if(!batch)return;openDialog('继续这批独立尝试',`<p class="prose">已确认 ${batch.index} / ${batch.plan.length} 项。已成功发布的任务会保留；继续发布使用原任务 ID 和请求 ID。</p><p class="form-error" role="alert"></p><div class="dialog-tools"><button id="continue-batch" class="primary">继续发布剩余项</button><button id="discard-batch" ${api.pending()?'disabled':''}>放弃未提交的剩余项</button></div>${api.pending()?'<p class="subtle">存在未知结果，请先在底部核对原操作，再决定是否放弃。</p>':''}`,'BATCH');content.querySelector('#continue-batch').onclick=async e=>{e.currentTarget.disabled=true;try{await exclusive(resumeBatch);}catch(error){content.querySelector('.form-error').textContent=errorText(error);content.querySelector('#continue-batch').disabled=false;await refresh().catch(()=>{});}};content.querySelector('#discard-batch').onclick=()=>{if(api.pending())return;api.save('batch',null);closeDialog();pendingControls();toast('已放弃剩余项；成功发布的任务保留');};}
async function taskAction(action,t){
  if(action==='replay'||action==='chain'){taskView(t,action==='replay'?'task':'chain');return;}
  if(['child','variant','retry'].includes(action)){createDialog(t,action);return;}
  if(action==='history'){await showHistory(t);return;}
  if(action==='policy'){openDialog('指定领取对象',form(targetSelect(t.application.claim_policy.target),'更新领取对象'),t.id);bindForm(async data=>{await api.action('claim_policy.update',{task:t.ref,expected_task_revision:t.revision,claim_policy:data.target?{mode:'target',target:{kind:'agent',id:data.target}}:{mode:'workspace'}},t.application.policy_revision);await afterWrite('领取资格已更新，尚未执行');showTask(t.id);});return;}
  if(action==='revise'){if(Object.keys(t.expectation.extensions||{}).some(k=>k!=='io.taskledger.ui')){toast('此任务包含外部业务扩展，请用保留精确 JSON 的 Skill 修订；页面不重写未知扩展。');return;}openDialog('修订任务预期',form(input('goal','目标',t.goal)+area('expected','预期结果',t.expectedResult)+area('reason','修改理由'),'修订预期'),t.id);bindForm(async data=>{const body={goal:data.goal,expected_result:data.expected,reason:data.reason};for(const key of ['delivery_requirements','constraints','inputs','context'])if(t.expectation[key])body[key]=t.expectation[key];if(t.expectation.extensions&&Object.keys(t.expectation.extensions).length)body.extensions=t.expectation.extensions;await api.execute('task.revise',t,body);await afterWrite('预期已修订；已有执行依据保持不变');showTask(t.id);});return;}
  const titles={claim:'领取任务',start:'开始执行',report:'记录进展',submit:'提交交付',accept:'接受这份交付',reject:'拒绝这份交付',reopen:'重新开放任务',release:'释放当前承接',fail:'报告执行失败',cancel:'取消任务'};
  const simple=['claim','start'].includes(action);
  let fields=simple?`<p class="prose">${action==='claim'?'以当前身份领取；本标签页将保留独立的执行会话证明。':'确认采用当前预期，开始本次执行。'}</p>`:area('text',action==='report'?'进展与证据':action==='submit'?'交付摘要':'决定或操作理由');
  if(action==='submit')fields+=input('artifact','产物引用（可选）','',{required:false,placeholder:'https://… 或资源 URI'});
  if(action==='accept'||action==='reject'){const submission=t.submissions.find(s=>s.submission_id===t.task.pending_submission_id);fields=`<section class="detail-section"><h3>待验收交付</h3><p class="prose">${escape(submission?.summary)}</p></section>`+fields;}
  if(['report','submit','start','fail'].includes(action)&&!api.sessionFor(t))fields='<p class="field-help">当前执行由其他客户端持有。请让该客户端记录进展或交付；网页不会借用其执行会话。</p>'+fields;
  openDialog(titles[action],form(fields,titles[action]),t.id);
  bindForm(async data=>{
    let operation,body={},application={},session=null;
    if(action==='claim'){session=await api.ensureSession(t);operation='task.claim';application={session_id:session.session_id};}
    else if(['start','report','submit','fail'].includes(action)){
      session=api.executionSession(t);body=runBody(t);operation=`run.${action}`;
      if(action==='start')body.basis_version=t.task.expectation_version;
      if(action==='report')body.message=data.text;
      if(action==='submit'){body.basis_version=t.run.basis_version;body.summary=data.text;if(data.artifact?.trim())body.artifacts=[{uri:data.artifact.trim()}];}
      if(action==='fail')body.reason=data.text;
    }else if(['accept','reject'].includes(action)){operation='task.decide';body={submission_id:t.task.pending_submission_id,outcome:action==='accept'?'accepted':'rejected',reason:data.text};}
    else if(action==='release'){operation='assignment.release';body={...runBody(t),reason:data.text};delete body.run_id;if(!sameActor(t.issuer,api.identity.actor))session=api.executionSession(t);}
    else{operation=`task.${action}`;body={reason:data.text};}
    await api.execute(operation,t,body,application,session);
    await afterWrite(action==='submit'?'交付已提交，等待发起者验收':'操作已记录');
    if(['submit','fail','release','cancel'].includes(action))try{await api.endSession(t,action==='fail'?'failed':action==='cancel'?'cancelled':'done');}catch(e){notice('任务操作已成功，会话结束尚需核对。'+errorText(e));}
    showTask(t.id);
  });
}
function projectDialog(){openDialog('创建项目',form(input('title','项目名称')+area('description','项目说明'),'创建项目'),'PROJECT');bindForm(async data=>{const projectId=id();await api.action('project.create',{project_id:projectId,title:data.title,description:data.description});state.project=projectId;closeDialog();await afterWrite('项目已创建');});}
function participantDialog(){
  const participants=state.workspace.participants;
  openDialog('协作参与者',`<p class="subtle">登记稳定身份与能力资料。凭据仍由管理者在服务配置中带外分发。</p><div class="stack">${participants.map(p=>`<div class="menu-row"><span>${escape(p.display_name)} <small>${p.actor.kind} · ${escape(p.actor.id)}</small></span><span class="badge">${p.state==='enabled'?'启用':'只读'}</span></div>`).join('')}</div>${canAdmin()?'<button id="add-participant" class="primary submit">登记参与者</button>':''}`,'PARTICIPANTS','participants');
  content.querySelector('#add-participant')?.addEventListener('click',()=>{openDialog('登记参与者',form(`<label for="actor-kind">主体类型</label><select id="actor-kind" name="kind"><option value="agent">Agent</option><option value="human">人</option></select>${input('id','稳定身份 ID')}${input('name','显示名称')}${input('provider','客户端 / provider','',{required:false})}${input('model','模型（可选）','',{required:false})}`,'登记身份'),'PARTICIPANT');bindForm(async data=>{const profile={};if(data.provider)profile.provider=data.provider;if(data.model)profile.model=data.model;await api.action('participant.create',{actor:{kind:data.kind,id:data.id},display_name:data.name,state:'enabled',profile});await afterWrite('身份已登记；凭据仍需带外配置');participantDialog();});});
}
function workspaceDialog(){if(!state.connected){connectionDialog();return;}openDialog('工作空间',`<div class="stack">${state.workspace.projects.map(p=>`<button class="menu-row project-link" data-id="${escape(p.project_id)}"><span>${escape(p.title)}</span><small>${state.tasks.filter(t=>t.application.project_id===p.project_id).length} 项任务</small></button>`).join('')}</div><div class="dialog-tools">${canWrite()?'<button id="add-project">＋ 项目</button>':''}<button id="participants">参与者</button><button id="switch-account">切换身份</button><button id="disconnect">断开连接</button></div><p class="subtle">当前：${escape(actorName(api.identity.actor))} · ${escape(api.identity.actor.kind)}<br>Agent 由当前会话发起，不由网页自动启动。</p>`,'WORKSPACE','workspace');content.querySelectorAll('.project-link').forEach(b=>b.onclick=()=>{closeDialog();setProject(b.dataset.id);});content.querySelector('#add-project')?.addEventListener('click',projectDialog);content.querySelector('#participants').onclick=participantDialog;content.querySelector('#switch-account').onclick=connectionDialog;content.querySelector('#disconnect').onclick=()=>{if(working||api.busy){toast('操作正在提交，请等待回执后再断开连接。');return;}state.epoch++;state.connected=false;api.token='';sessionStorage.removeItem('task-ledger:token');state.tasks=[];state.workspace=null;state.cursor=null;state.events=[];state.seen.clear();history.reset();if(taskHistory){taskHistory.reset();taskHistory=null;taskHistoryId=null;}closeDialog();draw();$('#connection').className='connection';$('#connection span').textContent='未连接';connectionDialog();};}
function searchDialog(){if(!state.connected)return connectionDialog();openDialog('寻找一条任务轨迹','<input id="task-search" class="search-input" aria-label="搜索任务" placeholder="任务名称、ID、状态或承接者…"><div id="search-results"></div>','QUICK FIND','search');const input=content.querySelector('input'),results=content.querySelector('#search-results');const render=()=>{const term=input.value.toLocaleLowerCase();const found=state.tasks.filter(t=>[t.title,t.id,STATES[t.state],actorName(t.assignee)].join(' ').toLocaleLowerCase().includes(term)).slice(0,40);results.innerHTML=found.map(t=>`<button class="menu-row" data-id="${escape(t.id)}"><span>${escape(t.title)}</span><small>${STATES[t.state]}</small></button>`).join('')||'<p class="subtle">没有匹配的任务。</p>';results.querySelectorAll('button').forEach(b=>b.onclick=()=>showTask(b.dataset.id));};input.oninput=render;render();}
async function showHistory(task=null){
  openDialog(task?'任务时间线':'工作空间事件','<p class="subtle">正在读取已记录的事件…</p>',task?.id||'EVENT LOG','history');
  const holder=content;
  try{const opened=await api.query('events.open',task?{task:task.ref}:{});if(state.dialog!=='history')return;let cursor=opened.cursor;const list=document.createElement('div');list.className='event-history';const more=document.createElement('button');more.textContent='加载更多';holder.querySelector('.subtle').replaceWith(list);holder.append(more);async function load(){more.disabled=true;try{const page=await api.query('events.read',{cursor,limit:100});for(const e of page.events){const row=document.createElement('article');row.className='history-row';row.innerHTML=`<div class="history-top"><span>${escape(OPERATIONS[e.event.operation]||e.event.operation)}</span><time>${escape(e.recorded_at)}</time></div><div class="prose">${escape(actorName(e.event.actor))} · ${escape(e.event.task?.task_id||e.event.entity_id||'')} ${e.event.revision?'· r'+e.event.revision:''}</div>${e.event.reason?`<p class="prose">${escape(e.event.reason)}</p>`:''}`;list.append(row);}cursor=page.next_cursor;more.hidden=page.done;if(!list.childElementCount)list.textContent='尚无事件';}catch(e){toast(errorText(e));}finally{more.disabled=false;}}more.onclick=load;await load();}catch(e){if(state.dialog==='history')content.querySelector('.subtle').textContent=errorText(e);}
}

$('#connection').onclick=connectionDialog;$('#workspace').onclick=workspaceDialog;$('#search').onclick=searchDialog;$('#create').onclick=()=>createDialog();$('#empty-create').onclick=()=>createDialog();$('#project-select').onchange=e=>setProject(e.target.value);$('#state-select').onchange=e=>{state.filter=e.target.value;draw();};$('#timeline').onclick=()=>state.connected&&showHistory();$('#refresh').onclick=()=>refresh().catch(e=>notice(errorText(e)));$('#batch-resume').onclick=batchDialog;$('#pending').onclick=async()=>{try{await exclusive(async()=>{await api.retry();await afterWrite('已取得原操作回执，并重新读取当前事实');});}catch(e){notice(errorText(e));}finally{pendingControls();}};
dialog.addEventListener('cancel',()=>{state.dialog=null;graph.setPlaybackSuspended(false);});dialog.addEventListener('click',e=>{if(e.target===dialog){const r=dialog.getBoundingClientRect();if(e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom)closeDialog();}});
document.addEventListener('keydown',e=>{if((e.metaKey||e.ctrlKey)&&e.key==='k'&&!dialog.open){e.preventDefault();searchDialog();}});
$('#back-overview').onclick=()=>selectView('overview');$('#back-chain').onclick=()=>selectView('chain',state.chainId,state.focusId);
$('#choose-chain').onclick=chainDialog;$('#choose-focus').onclick=focusDialog;
$('#empty-reset').onclick=()=>{state.filter='';state.activity='all';state.overviewPage=0;$('#state-select').value='';$('#activity-select').value='all';draw();graph.fit();};
$('#activity-select').onchange=e=>{state.activity=e.target.value;state.overviewPage=0;draw();graph.fit();};
$('#overview-prev').onclick=()=>{state.overviewPage--;draw();graph.fit();};$('#overview-next').onclick=()=>{state.overviewPage++;draw();graph.fit();};
const overviewResize=new ResizeObserver(()=>{if(state.view==='overview'&&overviewCapacity()!==state.overviewPageSize){draw();graph.fit();}});overviewResize.observe($('#graph'));
document.addEventListener('visibilitychange',()=>{if(!document.hidden)poll();});
setInterval(()=>{poll();if(state.connected&&!document.hidden){void history.read();if(taskHistory)void taskHistory.read();}},1800);
const saved=sessionStorage.getItem('task-ledger:token');
if(saved){exclusive(()=>connect(saved)).catch(e=>{notice(errorText(e));connectionDialog();});}else connectionDialog();
window.addEventListener('pagehide',()=>{overviewResize.disconnect();graph.destroy();},{once:true});
