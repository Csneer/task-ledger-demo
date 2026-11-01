import test from 'node:test';
import assert from 'node:assert/strict';
import {normalize,allowed,createPlan,safeLink} from './model.js';
const actor={kind:'human',id:'owner'};
const task={authority_id:'a',task_id:'t',issuer:actor,state:'submitted',revision:5,expectation_version:1,expectations:[{version:1,goal:'Review',expected_result:'Evidence',extensions:{}}],generation:1,current_assignment_id:null,current_run_id:null,pending_submission_id:'s',relations:[]};
test('submission remains pending and only immutable issuer may decide',()=>{
 const t=normalize({snapshot:{task,records:[]},application:{claim_policy:{mode:'workspace'}}});
 assert.equal(t.state,'submitted');
 assert.equal(allowed(t,{actor,write:true},[{actor,state:'enabled'}]).decide,true);
 const other={kind:'agent',id:'owner'};
 assert.equal(allowed(t,{actor:other,write:true,admin:true},[{actor:other,state:'enabled'}]).decide,false);
 assert.equal(allowed(t,{actor,write:true},[{actor,state:'read_only'}]).decide,false);
});
test('parallel draws have distinct task and command IDs with related references',()=>{
 const plan=createPlan({authority:'a',projectId:'p',goal:'Explore',expectedResult:'Evidence',parent:{ref:{authority_id:'a',task_id:'p'}},kind:'variant',count:3});
 assert.equal(new Set(plan.map(x=>x.command.task.task_id)).size,3);
 assert.equal(new Set(plan.map(x=>x.command.command_id)).size,3);
 assert.ok(plan.every(x=>x.command.body.relations[0].kind==='related'));
 assert.equal(new Set(plan.map(x=>x.command.body.extensions['io.taskledger.ui'].group_id)).size,1);
 assert.throws(()=>createPlan({authority:'a',projectId:'p',goal:'x',expectedResult:'x',count:7}));
});
test('completed tasks cannot reopen; children refer to parent without altering it',()=>{
 const t=normalize({snapshot:{task:{...task,state:'completed'},records:[]},application:{claim_policy:{mode:'workspace'}}});
 assert.equal(allowed(t,{actor,write:true},[{actor,state:'enabled'}]).reopen,false);
 const plan=createPlan({authority:'a',projectId:'p',goal:'Child',expectedResult:'Done',parent:t});
 assert.deepEqual(plan[0].command.body.relations,[{kind:'parent',target:t.ref}]);
 assert.equal(t.state,'completed');
});
test('artifact links do not execute local or script schemes',()=>{assert.equal(safeLink('javascript:alert(1)'),null);assert.equal(safeLink('file:///etc/passwd'),null);assert.equal(safeLink('https://example.com/a'),'https://example.com/a');});
