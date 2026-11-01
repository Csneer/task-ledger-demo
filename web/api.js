import {BINDING,PROTOCOL,command,id} from './model.js';

export class APIError extends Error {
  constructor(code,status=0,unknown=false){super(code);this.code=code;this.status=status;this.unknown=unknown;}
}
export class LedgerAPI {
  constructor(storage=sessionStorage){this.storage=storage;this.token='';this.scope='';this.identity=null;this.info=null;this.busy=false;this.recentEvents=[];}
  async request(path,body,session=null,method='POST') {
    const headers={Authorization:`Bearer ${this.token}`};if(body)headers['Content-Type']='application/json';
    if(session){headers['X-Ledger-Session']=session.session_id;headers['X-Ledger-Session-Proof']=session.secret;}
    const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),15000);
    try {
      const response=await fetch(path,{method,headers,body:body?JSON.stringify(body):undefined,signal:controller.signal,cache:'no-store',redirect:'error'});
      let data;try{data=await response.json();}catch{throw new APIError('响应不可解析',response.status,true);}
      if(!response.ok)throw new APIError(data.error?.code||data.error?.error?.kind||`HTTP_${response.status}`,response.status,response.status>=500);
      return data;
    }catch(e){if(e instanceof APIError)throw e;throw new APIError('连接中断或请求超时',0,true);}finally{clearTimeout(timer);}
  }
  async connect(token) {
    if(this.busy)throw new APIError('操作正在提交，请等待回执后再切换身份');
    this.token=token.trim();this.identity=await this.request('/v1/identity',null,null,'GET');
    this.info=await this.query('service.info');
    if(this.info.binding_version!==BINDING||this.info.protocol_version!==PROTOCOL)throw new APIError('服务协议不兼容');
    this.scope=JSON.stringify([location.origin,this.info.authority_id,this.identity.actor.kind,this.identity.actor.id]);
    this.storage.setItem('task-ledger:token',this.token);
    return this.info;
  }
  read(key,fallback=null){try{return JSON.parse(this.storage.getItem(`${this.scope}:${key}`))??fallback;}catch{return fallback;}}
  save(key,value){this.storage.setItem(`${this.scope}:${key}`,JSON.stringify(value));}
  async query(operation,body={}){return (await this.request('/v1/queries',{binding_version:BINDING,operation,body})).result;}
  pending(){return this.read('pending');}
  async write(path,body,session=null) {
    if(this.busy)throw new APIError('另一个操作正在提交');
    const pending=this.pending();
    if(pending&&JSON.stringify(pending.body)!==JSON.stringify(body))throw new APIError('请先核对未确认的操作');
    const intent=pending||{path,body,session};
    this.save('pending',intent); // If storage fails, do not send an unrecorded write.
    this.busy=true;
    try {const result=await this.request(intent.path,intent.body,intent.session);this.save('pending',null);if(result.result?.event&&!result.result.replayed)this.recentEvents.push(result.result.event);return result;}
    catch(e){if(!pending&&!e.unknown)this.save('pending',null);throw e;}finally{this.busy=false;}
  }
  async retry(){const p=this.pending();if(!p)return null;return this.write(p.path,p.body,p.session);}
  async action(operation,body,revision=0,session=null,requestId=id()) {
    return this.write('/v1/actions',{binding_version:BINDING,request_id:requestId,operation,expected_revision:revision,body},session);
  }
  async execute(operation,task,body,application={},session=null){return this.write('/v1/commands',command(operation,task,body,application),session);}
  sessionFor(task){return this.read(`session:${task.id}`);}
  async ensureSession(task) {
    let session=this.sessionFor(task);
    if(session?.begun){const current=await this.query('session.get',{session_id:session.session_id});if(current.session.state==='ended'){session=null;this.save(`session:${task.id}`,null);}}
    if(!session){const bytes=crypto.getRandomValues(new Uint8Array(32));const secret=btoa(String.fromCharCode(...bytes)).replaceAll('+','-').replaceAll('/','_').replaceAll('=','');const digest=Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',bytes)),x=>x.toString(16).padStart(2,'0')).join('');session={session_id:id(),secret,digest,request_id:id(),begun:false};this.save(`session:${task.id}`,session);}
    if(!session.begun){await this.action('session.begin',{session_id:session.session_id,project_id:task.application.project_id,goal:task.goal,proof_digest:session.digest,provider:'web'},0,null,session.request_id);session.begun=true;this.save(`session:${task.id}`,session);}
    return session;
  }
  executionSession(task) {
    const session=this.sessionFor(task);
    if(!session||session.session_id!==task.application.execution_binding?.session_id)throw new APIError('当前执行属于其他客户端；请由该客户端报告或交付');
    return session;
  }
  async endSession(task,outcome='done') {
    const session=this.sessionFor(task);if(!session)return;
    const current=await this.query('session.get',{session_id:session.session_id});
    if(current.session.state==='active'&&!current.current_binding)await this.action('session.end',{session_id:session.session_id,outcome,summary:'网页执行结束；任务状态由交付与验收决定'},current.session.revision,session);
    this.save(`session:${task.id}`,null);
  }
}
