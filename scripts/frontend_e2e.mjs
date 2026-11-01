#!/usr/bin/env node
// Real-browser checks against an explicitly supplied, isolated fixture.
import {chromium} from 'playwright';
import {readFile,writeFile} from 'node:fs/promises';
import {join,resolve} from 'node:path';
import assert from 'node:assert/strict';

const dir=process.argv[2];
if(!dir)throw new Error('Usage: node scripts/frontend_e2e.mjs /private/frontend-fixture-directory');
const config=JSON.parse(await readFile(join(dir,'server.json'),'utf8'));
const fixture=JSON.parse(await readFile(join(dir,'fixture.json'),'utf8'));
assert.match(config.listen,/^127\.0\.0\.1:\d+$/);
const url=`http://${config.listen}`,token=(await readFile(join(dir,'owner-token'),'utf8')).trim();
const browser=await chromium.launch({executablePath:process.env.CHROME_PATH||'/usr/bin/google-chrome',headless:true});
const errors=[],checks=[];
const query=async(operation,body={})=>{const r=await fetch(url+'/v1/queries',{method:'POST',headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json'},body:JSON.stringify({binding_version:'ledger-http-draft-1',operation,body})});assert.equal(r.status,200);return(await r.json()).result;};
try{
 const page=await browser.newPage({viewport:{width:1440,height:1000}});
 page.on('pageerror',e=>errors.push(e.message));
 await page.goto(url);await page.getByLabel('访问凭据').fill(token);await page.getByRole('button',{name:'连接',exact:true}).click();
 await page.locator('#dialog').waitFor({state:'hidden'});
 assert.ok((await query('workspace.snapshot')).tasks.length>=5);checks.push('authenticated same-origin workspace');
 await page.waitForTimeout(1200);
 await page.screenshot({path:join(dir,'frontend-desktop.png')});
 const close=async()=>{if(await page.locator('#dialog').isVisible())await page.getByRole('button',{name:'关闭弹窗'}).click();};
 const search=async text=>{await close();await page.locator('#search').click();await page.locator('#task-search').fill(text);await page.locator('#search-results button').filter({has:page.getByText(text,{exact:true})}).click();};
 const title='浏览器真实流转 '+Date.now();
 await page.locator('#create').click();await page.getByLabel('任务目标',{exact:true}).fill(title);await page.getByLabel('预期结果',{exact:true}).fill('记录领取、重做、交付与验收的可复现证据');await page.getByRole('button',{name:'发布任务',exact:true}).click();await page.locator('#dialog').waitFor({state:'hidden'});
 let ws=await query('workspace.snapshot');const created=ws.tasks.find(b=>b.snapshot.task.expectations[0].goal===title);assert.ok(created);const ref={authority_id:config.authority,task_id:created.snapshot.task.task_id};
 const fact=async()=>normalizeSnapshot(await query('task.snapshot',{task:ref}));
 const doAction=async(action,text)=>{await page.locator(`[data-action="${action}"]`).click();if(text)await page.locator('textarea[name=text]').fill(text);await page.locator('form button[type=submit]').click();await page.locator('.task-actions').waitFor();assert.equal(await page.locator('.form-error').count(),0);};
 await search(title);
 const beforeRevision=await fact();const revised=await fetch(url+'/v1/commands',{method:'POST',headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json'},body:JSON.stringify({binding_version:'ledger-http-draft-1',command:{protocol_version:'task-core-draft-1',command_id:crypto.randomUUID(),task:ref,expected_revision:beforeRevision.revision,operation:'task.revise',body:{goal:title,expected_result:'外部客户端已更新的预期',reason:'验证打开详情时的真实事件刷新'}},application:{}})});assert.equal(revised.status,200);
 await page.getByText('外部客户端已更新的预期',{exact:true}).waitFor({timeout:8000});
 await page.locator('[data-action=revise]').click();await page.locator('textarea[name=expected]').fill('保留本地未提交的编辑');
 const nextSnapshot=page.waitForResponse(r=>r.url().endsWith('/v1/queries')&&r.request().postDataJSON().operation==='workspace.snapshot');
 const revision=(await fact()).revision;const externalEdit=await fetch(url+'/v1/commands',{method:'POST',headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json'},body:JSON.stringify({binding_version:'ledger-http-draft-1',command:{protocol_version:'task-core-draft-1',command_id:crypto.randomUUID(),task:ref,expected_revision:revision,operation:'task.revise',body:{goal:title,expected_result:'表单打开期间的外部更新',reason:'验证保留用户未提交输入'}},application:{}})});assert.equal(externalEdit.status,200);
 await nextSnapshot;await page.waitForTimeout(150);assert.equal(await page.locator('textarea[name=expected]').inputValue(),'保留本地未提交的编辑');await search(title);await page.getByText('表单打开期间的外部更新',{exact:true}).waitFor();checks.push('open detail follows external events without overwriting forms');
 // Hold the read AFTER a successful claim: the API write lock is no longer
 // held, but the complete UI workflow must still prevent identity changes.
 let resumeRead,readStarted,holdRead=true;const readGate=new Promise(resolve=>{resumeRead=resolve}),reading=new Promise(resolve=>{readStarted=resolve});
 await page.route('**/v1/queries',async route=>{if(holdRead&&route.request().postDataJSON().operation==='workspace.snapshot'){holdRead=false;readStarted();await readGate;}await route.continue();});
 const claiming=doAction('claim');await reading;await close();await page.locator('#workspace').click();await page.locator('#disconnect').click();assert.equal(await page.locator('.connection.online').count(),1);await page.getByText('操作正在提交，请等待回执后再断开连接。',{exact:true}).waitFor();await close();await page.locator('#connection').click();assert.equal(await page.locator('#dialog').isVisible(),false);
 resumeRead();await claiming;await page.unroute('**/v1/queries');assert.equal((await fact()).state,'claimed');checks.push('identity changes blocked throughout a multi-request write workflow');
 await doAction('start');assert.equal((await fact()).state,'active');
 await doAction('report','真实浏览器报告：公开接口与任务画布已连通。');
 await doAction('submit','第一份交付，供拒绝和重做路径验证。');assert.equal((await fact()).state,'submitted');
 await doAction('reject','需要补充更完整的验证记录');assert.equal((await fact()).state,'rejected');
 await doAction('reopen','重新执行，保留前一份交付与决定');assert.equal((await fact()).state,'offered');
 await doAction('claim');await doAction('start');await doAction('submit','第二次交付，包含重新执行和浏览器验证证据。');
 await doAction('accept','隔离浏览器测试已核对第二次结果');let current=await fact();assert.equal(current.state,'completed');assert.equal(current.generation,2);checks.push('claim/start/report/submit/reject/reopen/new assignment/accept');
 assert.equal(await page.locator('[data-submission]').count(),2);await page.locator('[data-submission]').filter({hasText:'第二次交付'}).getByText('已接受',{exact:false}).waitFor();checks.push('each submission displays its own decision');
 await page.screenshot({path:join(dir,'frontend-completed-detail.png')});
 await page.locator('[data-action=retry]').click();await page.locator('input[name=goal]').fill(title+' 新一轮');await page.locator('form button[type=submit]').click();await page.locator('#dialog').waitFor({state:'hidden'});assert.equal((await fact()).state,'completed');checks.push('terminal retry creates a related task');
 await search(title);await page.locator('[data-action=child]').click();await page.locator('input[name=goal]').fill(title+' 子任务');await page.locator('textarea[name=expected]').fill('独立交付');await page.locator('form button[type=submit]').click();await page.locator('#dialog').waitFor({state:'hidden'});
 await search(title+' 子任务');await page.locator('[data-action=child]').click();await page.locator('input[name=goal]').fill(title+' 深层子任务');await page.locator('textarea[name=expected]').fill('二级独立交付');await page.locator('form button[type=submit]').click();await page.locator('#dialog').waitFor({state:'hidden'});checks.push('child and deeper child via public create');
 await search(title);await page.locator('[data-action=variant]').click();await page.locator('input[name=count]').fill('3');await page.locator('form button[type=submit]').click();await page.locator('#dialog').waitFor({state:'hidden'});
 ws=await query('workspace.snapshot');const variants=ws.tasks.filter(b=>b.snapshot.task.expectations.at(-1).goal.startsWith(title+' · 尝试'));
 assert.equal(variants.length,3);assert.equal(new Set(variants.map(b=>b.snapshot.task.task_id)).size,3);assert.ok(variants.every(b=>b.snapshot.task.state==='offered'&&b.snapshot.task.relations[0].kind==='related'));checks.push('three independent related draws');
 // Simulate a lost successful response, not a rejected write. Retry must use
 // the same persisted command and create exactly one task.
 let drop=true;await page.route('**/v1/commands',async route=>{const request=route.request().postDataJSON();if(drop&&request.command.operation==='task.create'){drop=false;await route.fetch();await route.abort('failed');}else await route.continue();});
 await page.locator('#create').click();const uncertain=title+' 响应丢失';await page.locator('input[name=goal]').fill(uncertain);await page.locator('textarea[name=expected]').fill('同键重试不重复');await page.locator('form button[type=submit]').click();await page.locator('.form-error').filter({hasText:'结果尚未确认'}).waitFor();await close();await page.locator('#pending').click();await page.locator('#pending').waitFor({state:'hidden'});await page.locator('#batch-resume').click();await page.locator('#continue-batch').click();await page.locator('#dialog').waitFor({state:'hidden'});
 ws=await query('workspace.snapshot');assert.equal(ws.tasks.filter(b=>b.snapshot.task.expectations[0].goal===uncertain).length,1);checks.push('lost response and exact retry create one task');await page.unroute('**/v1/commands');
 await page.reload();await page.locator('.connection.online').waitFor();checks.push('tab credential and snapshot restore');
 await search(title);await page.locator('[data-action=replay]').click();await page.locator('.tg-history-status').filter({hasText:'事件顺序'}).waitFor();
 const expectedHistory=['发布任务','修订预期','修订预期','领取任务','开始执行','进展报告','提交交付','退回修改','重做任务','领取任务','开始执行','提交交付','验收通过'];
 assert.deepEqual(await page.locator('.tg-history-step').evaluateAll(buttons=>buttons.map(b=>b.title.split(' · ').at(-1))),expectedHistory);checks.push('reconnected completed task replays the persisted order including rejection and rework');
 const playback=page.locator('.tg-history-current');const firstStep=await playback.textContent();await page.waitForFunction(first=>document.querySelector('.tg-history-current').textContent!==first,firstStep,{timeout:6000});
 await page.locator('[data-action=history-play]').click();const paused=await playback.textContent();await page.waitForTimeout(2800);assert.equal(await playback.textContent(),paused);assert.equal((await fact()).state,'completed');checks.push('historical playback advances, pauses across polling, and never changes current task state');
 await page.locator('.tg-history-step').last().click();assert.match(await playback.textContent(),/验收通过/);const completedNode=page.locator(`.tg-node[data-task-id="${ref.task_id}"]`);assert.equal(await completedNode.getAttribute('data-state'),'completed');
 assert.notEqual(await completedNode.locator('.tg-ring').evaluate(node=>getComputedStyle(node,'::before').animationName),'none');await page.screenshot({path:join(dir,'frontend-history-replay.png')});
 await page.locator('#back-chain').click();assert.ok(await page.locator('.tg-history-step').count()>expectedHistory.length);checks.push('completed orbit stays animated and task replay can return to its task chain');
 await page.setViewportSize({width:390,height:844});await page.waitForTimeout(200);assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);await page.screenshot({path:join(dir,'frontend-mobile.png')});
 const historyBounds=await page.locator('.tg-history').boundingBox(),zoomBounds=await page.locator('.tg-controls').boundingBox();assert.ok(historyBounds.y+historyBounds.height<=zoomBounds.y);checks.push('390px historical playback and zoom controls remain separate');
 await search(title);const bounds=await page.locator('#dialog').boundingBox();assert.ok(bounds.x>=0&&bounds.x+bounds.width<=391);await close();checks.push('390px navigation and detail dialog');
 assert.deepEqual(errors,[]);checks.push('no page JavaScript errors');
 const reduced=await browser.newPage({viewport:{width:1000,height:800},reducedMotion:'reduce'});await reduced.goto(url);await reduced.getByLabel('访问凭据').fill(token);await reduced.getByRole('button',{name:'连接',exact:true}).click();await reduced.locator('#dialog').waitFor({state:'hidden'});await reduced.locator('#search').click();await reduced.locator('#task-search').fill(ref.task_id);await reduced.locator('#search-results button').click();await reduced.locator('[data-action=replay]').click();await reduced.locator('.tg-history-step').first().waitFor();assert.match(await reduced.locator('[data-action=history-play]').textContent(),/播放/);assert.equal(await reduced.locator('.tg-node.has-history .tg-ring').first().evaluate(node=>getComputedStyle(node,'::before').animationName),'none');await reduced.close();checks.push('reduced-motion keeps numbered history available and starts paused');
 await writeFile(join(dir,'browser-results.json'),JSON.stringify({passed:true,url,checks,errors,taskId:ref.task_id,build:fixture.build||null,at:new Date().toISOString()},null,2),{mode:0o600});
 console.log(JSON.stringify({passed:true,checks,evidence:resolve(dir)},null,2));
}finally{await browser.close();}
function normalizeSnapshot(value){return value.bundle.snapshot.task;}
