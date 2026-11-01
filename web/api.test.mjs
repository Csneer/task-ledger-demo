import test from 'node:test';
import assert from 'node:assert/strict';
import {LedgerAPI,APIError} from './api.js';
const memory=()=>{const data=new Map();return {getItem:k=>data.get(k)||null,setItem:(k,v)=>data.set(k,v)}};
test('unknown write persists exact intent and retry does not generate a new command',async()=>{
 const store=memory(),api=new LedgerAPI(store);api.scope='a:human';
 const body={command:{command_id:'fixed',expected_revision:4,body:{message:'original'}}};
 let first;api.request=async(path,payload,session)=>{first={path,payload,session};throw new APIError('lost',0,true)};
 await assert.rejects(api.write('/v1/commands',body,{secret:'private'}));
 assert.deepEqual(api.pending().body,body);
 await assert.rejects(api.write('/v1/commands',{command:{command_id:'new'}}),/先核对/);
 const recovered=new LedgerAPI(store);recovered.scope=api.scope;
 recovered.request=async(path,payload,session)=>{assert.deepEqual({path,payload,session},first);return {result:{replayed:true,event:{event_id:'old'}}};};
 await recovered.retry();assert.equal(recovered.pending(),null);assert.equal(recovered.recentEvents.length,0);
});
test('known conflict clears pending and failed persistence sends no write',async()=>{
 const api=new LedgerAPI(memory());api.request=async()=>{throw new APIError('STALE_REVISION',409,false)};
 await assert.rejects(api.write('/v1/commands',{x:1}));assert.equal(api.pending(),null);
 const broken=new LedgerAPI({getItem:()=>null,setItem:()=>{throw new Error('quota')}});let sent=false;broken.request=async()=>{sent=true};
 await assert.rejects(broken.write('/v1/actions',{}));assert.equal(sent,false);
});
test('another client execution cannot be impersonated with a session ID',()=>{
 const api=new LedgerAPI(memory());const task={id:'t',application:{execution_binding:{session_id:'agent-session'}}};
 api.save('session:t',{session_id:'browser-session',secret:'x'});
 assert.throws(()=>api.executionSession(task),/其他客户端/);
});
test('recovery denial preserves the original uncertain intent until a receipt arrives',async()=>{
 const api=new LedgerAPI(memory()),body={command:{command_id:'uncertain',operation:'task.create'}};
 api.request=async()=>{throw new APIError('lost',0,true)};
 await assert.rejects(api.write('/v1/commands',body));
 const intent=api.pending();
 for(const code of ['UNAUTHENTICATED','FORBIDDEN','STALE_REVISION']){
  api.request=async()=>{throw new APIError(code,403,false)};
  await assert.rejects(api.retry());assert.deepEqual(api.pending(),intent);
 }
 api.request=async()=>({result:{replayed:true}});
 await api.retry();assert.equal(api.pending(),null);
});
test('identity cannot change while a write is waiting for its receipt',async()=>{
 const api=new LedgerAPI(memory());api.token='original';api.scope='original-scope';
 let finish;api.request=()=>new Promise(resolve=>{finish=resolve});
 const writing=api.write('/v1/commands',{command:{command_id:'in-flight'}});
 await assert.rejects(api.connect('replacement'),/操作正在提交/);
 assert.equal(api.token,'original');assert.equal(api.scope,'original-scope');
 finish({result:{}});await writing;
});
