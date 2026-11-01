#!/usr/bin/env node
// Read-only browser acceptance against the explicit loopback ux-100 fixture.
import {chromium} from 'playwright';
import {readFile,writeFile} from 'node:fs/promises';
import {join,resolve} from 'node:path';
import assert from 'node:assert/strict';

const dir=process.argv[2];
if(!dir)throw new Error('Usage: node scripts/usability_e2e.mjs /private/isolated-fixture-directory');
const config=JSON.parse(await readFile(join(dir,'server.json'),'utf8'));
const fixture=JSON.parse(await readFile(join(dir,'fixture.json'),'utf8'));
assert.match(config.listen,/^127\.0\.0\.1:\d+$/);
const url=`http://${config.listen}`,token=(await readFile(join(dir,'owner-token'),'utf8')).trim();
const query=async(operation,body={})=>{
  const response=await fetch(url+'/v1/queries',{method:'POST',headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json'},body:JSON.stringify({binding_version:'ledger-http-draft-1',operation,body})});
  assert.equal(response.status,200);return(await response.json()).result;
};
const before=await query('workspace.snapshot');
const bundles=before.tasks.filter(t=>t.application.project_id==='ux-100');
assert.equal(bundles.length,100,'Seed scripts/usability_fixture.mjs first');
const facts=ws=>ws.tasks.map(b=>[b.snapshot.task.task_id,b.snapshot.task.state,b.snapshot.task.revision]).sort();
const chain=Array.from({length:5},(_,i)=>`ux100-c01-t${i+1}`);
const expectedGroups=Array.from({length:20},(_,i)=>'group:'+encodeURIComponent(JSON.stringify([config.authority,`ux100-c${String(i+1).padStart(2,'0')}-t1`]))).sort();
let cursor=(await query('events.open')).cursor,recorded=[];
for(let i=0;i<50;i++){
  const batch=await query('events.read',{cursor,limit:200});recorded.push(...batch.events.filter(e=>e.source==='core'&&chain.includes(e.event.task.task_id)));
  if(batch.done)break;cursor=batch.next_cursor;if(i===49)throw new Error('History exceeded test budget');
}
const browser=await chromium.launch({executablePath:process.env.CHROME_PATH||'/usr/bin/google-chrome',headless:true});
const page=await browser.newPage({viewport:{width:1440,height:1000}}),checks=[],errors=[];let writes=0;
page.on('pageerror',e=>errors.push(e.message));
page.on('request',r=>{if(/\/v1\/(commands|actions)$/.test(r.url()))writes++;});
const ids=async kind=>page.locator(`.tg-node[data-kind="${kind}"]`).evaluateAll(nodes=>nodes.map(n=>n.dataset.taskId).sort());
const transform=()=>page.locator('.tg-world').evaluate(n=>n.style.transform);
const current=()=>page.locator('.tg-history-step[aria-current=step]').getAttribute('data-history-index');
const close=()=>page.getByRole('button',{name:'关闭弹窗'}).click();
const pause=async()=>{if((await page.locator('[data-action=history-play]').textContent()).includes('暂停'))await page.locator('[data-action=history-play]').click();};
const play=async()=>{if((await page.locator('[data-action=history-play]').textContent()).includes('播放'))await page.locator('[data-action=history-play]').click();};
const advance=async old=>page.waitForFunction(index=>document.querySelector('.tg-history-step[aria-current=step]')?.dataset.historyIndex!==index,old,{timeout:6500});
async function safeNode(){
  await page.waitForFunction(()=>{
    const n=document.querySelector('.tg-node.is-history-current'),v=document.querySelector('.tg-viewport').getBoundingClientRect(),h=document.querySelector('.tg-history').getBoundingClientRect();
    return n&&['.tg-orb','.tg-title','.tg-state','.tg-history-phase'].every(s=>{const r=n.querySelector(s).getBoundingClientRect();return r.left>=v.left&&r.right<=v.right&&r.top>=v.top&&r.bottom<=h.top-10;});
  },null,{timeout:2500});
}
async function chooseChain(){
  await page.locator('#choose-chain').click();await page.locator('#chain-search').fill(chain[0]);await page.locator('#chain-results button').click();
  await page.waitForFunction(()=>document.querySelector('.tg-history-status')?.textContent.includes('事件顺序')&&!document.querySelector('.tg-history').hidden);
  assert.deepEqual(await ids('task'),chain);assert.equal(await ids('group').then(x=>x.length),0);
}
async function groupsOnPage(){
  await page.waitForFunction(()=>{const nodes=[...document.querySelectorAll('.tg-node[data-kind=group]')];return nodes.length&&nodes.every(n=>Number(getComputedStyle(n).opacity)>=.99);});
  const value=await ids('group');
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  const visible=await page.locator('.tg-node[data-kind=group]').evaluateAll(nodes=>{
    const view=document.querySelector('.tg-viewport').getBoundingClientRect();
    return nodes.every(n=>['.tg-title','.tg-orb','.tg-state'].every(s=>{const r=n.querySelector(s).getBoundingClientRect();return r.left>=view.left&&r.right<=view.right&&r.top>=view.top&&r.bottom<=view.bottom-50;}));
  });assert.ok(visible,'overview title, count and summary are in the visible canvas');return value;
}
try{
  await page.goto(url);await page.getByLabel('访问凭据').fill(token);await page.getByRole('button',{name:'连接',exact:true}).click();await page.locator('#dialog').waitFor({state:'hidden'});
  await page.locator('#project-select').selectOption('ux-100');await page.waitForTimeout(200);
  const first=await groupsOnPage();assert.equal(first.length,12);assert.ok(await page.locator('#overview-prev').isDisabled());
  await page.locator('#overview-next').click();const second=await groupsOnPage();assert.equal(second.length,8);assert.ok(await page.locator('#overview-next').isDisabled());assert.deepEqual([...first,...second].sort(),expectedGroups);
  checks.push('desktop pagination covers all 20 chains exactly once (12 + 8)');
  await page.locator('#overview-prev').click();await groupsOnPage();await page.screenshot({path:join(dir,'usability-overview-desktop.png')});
  await page.setViewportSize({width:390,height:844});await page.waitForTimeout(200);assert.equal(await page.locator('#overview-page').textContent(),'1 / 5');
  const mobile=[];for(let p=0;p<5;p++){const groupIds=await groupsOnPage();assert.equal(groupIds.length,4);mobile.push(...groupIds);if(p<4)await page.locator('#overview-next').click();}
  assert.deepEqual(mobile.sort(),expectedGroups);assert.ok(await page.locator('#overview-next').isDisabled());
  await page.screenshot({path:join(dir,'usability-overview-mobile.png')});
  checks.push('390px pagination covers all 20 chains without clipping or duplicates');
  await chooseChain();await pause();
  const titles=await page.locator('.tg-history-step').evaluateAll(nodes=>nodes.map(n=>n.title.split(' · ')[0]));assert.deepEqual(titles,recorded.map(e=>e.event.task.task_id));
  await page.locator('#state-select').selectOption('completed');assert.deepEqual(await ids('task'),[chain[4]]);assert.match(await page.locator('#project-caption').textContent(),/筛选 1\/5.*仅含当前可见任务/);assert.match(await page.locator('.tg-history strong').textContent(),/局部/);
  await page.locator('#state-select').selectOption('');assert.deepEqual(await ids('task'),chain);checks.push('chain membership and recorded order match service facts; filtering is explicitly partial');
  await page.locator('.tg-history-step').first().click();await safeNode();
  await page.locator('.tg-history-speed').selectOption('2');
  // Three distinct tasks must receive the current marker and camera in log order.
  let previousTask=chain[0];
  for(const target of chain.slice(1,3)){
    const targetBefore=await page.locator(`.tg-node[data-task-id="${target}"] .tg-orb`).boundingBox();assert.ok(targetBefore.x<0||targetBefore.x+targetBefore.width>390,'next target starts outside readable viewport');
    const oldTransform=await transform();await play();await page.locator('[data-action=history-play]').focus();
    await page.waitForFunction(id=>document.querySelector('.tg-node.is-history-current')?.dataset.taskId===id,target,{timeout:10000});
    await safeNode();assert.notEqual(await transform(),oldTransform);assert.equal(await page.locator('.tg-node.is-history-current').count(),1);
    const index=Number(await current()),event=recorded[index].event;assert.equal(event.task.task_id,target);assert.notEqual(target,previousTask);
    const detail=await page.locator('.tg-history-detail-content').textContent();const actor=before.participants.find(p=>p.actor.kind===event.actor.kind&&p.actor.id===event.actor.id);
    assert.ok(detail.includes(actor?.display_name||event.actor.id));assert.ok(detail.includes(event.task_snapshot.generation?`第 ${event.task_snapshot.generation} 轮承接`:'领取前'));
    assert.equal(await page.evaluate(()=>document.activeElement.dataset.action),'history-play');await pause();previousTask=target;
  }
  checks.push('automatic replay crosses three task nodes, moves camera into safe area and preserves keyboard focus');
  await play();
  const blank=await page.evaluate(()=>{const v=document.querySelector('.tg-viewport').getBoundingClientRect();for(let y=v.top+16;y<v.top+100;y+=20)for(let x=20;x<180;x+=25){const e=document.elementFromPoint(x,y);if(e?.closest('.tg-viewport')&&!e.closest('button'))return{x,y};}return null;});assert.ok(blank);
  await page.mouse.move(blank.x,blank.y);await page.mouse.down();await page.mouse.move(blank.x+95,blank.y+25,{steps:8});await page.mouse.up();
  assert.equal(await page.locator('[data-action=history-follow]').getAttribute('aria-pressed'),'false');
  const dragged=await transform(),step=await current();await advance(step);assert.equal(await transform(),dragged);
  await page.locator('[data-action=history-follow]').click();await safeNode();assert.equal(await page.locator('[data-action=history-follow]').getAttribute('aria-pressed'),'true');
  await page.mouse.move(blank.x,blank.y);await page.mouse.wheel(0,120);await page.waitForTimeout(100);assert.equal(await page.locator('[data-action=history-follow]').getAttribute('aria-pressed'),'false');await pause();
  checks.push('drag and zoom disable camera follow while automatic history continues; follow can resume');
  const paused=await current(),pausedTransform=await transform();await page.waitForTimeout(2800);assert.equal(await current(),paused);assert.equal(await transform(),pausedTransform);
  await page.locator('[data-action=history-next]').click();assert.notEqual(await current(),paused);await page.locator('[data-action=history-prev]').click();assert.equal(await current(),paused);
  checks.push('pause survives polling; previous and next navigate actual recorded steps');
  await page.locator('[data-action=history-follow]').click();await play();
  await page.locator('#create').click();await page.locator('input[name=goal]').fill('未提交的验收输入');await page.locator('textarea[name=expected]').fill('回放不得覆盖或抢焦点');
  const formStep=await current(),formTransform=await transform();await page.waitForTimeout(2800);assert.equal(await current(),formStep);assert.equal(await transform(),formTransform);assert.equal(await page.locator('textarea[name=expected]').inputValue(),'回放不得覆盖或抢焦点');assert.equal(await page.evaluate(()=>document.activeElement.name),'expected');
  await close();await advance(formStep);await pause();const stopped=await current();await page.locator('#create').click();await close();await page.waitForTimeout(2800);assert.equal(await current(),stopped);
  checks.push('editing suspends both playback and camera without losing input, focus or prior play/pause intent');
  await page.locator('#choose-focus').click();await page.locator('#focus-search').fill(chain[4]);await page.locator('#focus-results button').click();
  await page.locator(`.tg-node[data-task-id="${chain[4]}"]`).click();await page.locator('[data-action=replay]').click();
  await page.locator('.tg-history-status').filter({hasText:'事件顺序'}).waitFor();assert.deepEqual(await ids('task'),[chain[4]]);assert.ok(await page.locator('#state-select').isDisabled());
  assert.ok((await page.locator('.tg-history-step').evaluateAll(nodes=>nodes.map(n=>n.title))).every(t=>t.startsWith(chain[4]+' · ')));
  await page.locator('.tg-history-step').last().click();await safeNode();assert.equal(await page.locator('.tg-node .tg-state').textContent(),'当前：已完成');
  await page.locator('.tg-history-details summary').click();assert.match(await page.locator('.tg-history-detail-content').textContent(),/参与者：.*\n阶段：第 1 轮承接\n操作：验收通过/);await page.locator('.tg-history-details summary').click();
  const chip=await page.locator('.tg-history-step[aria-current=step]').evaluate(n=>{const r=document.createRange();r.selectNodeContents(n);const a=r.getBoundingClientRect(),b=n.getBoundingClientRect();return{readable:n.scrollWidth<=n.clientWidth,left:a.left>=b.left,right:a.right<=b.right,visible:b.left>=0&&b.right<=innerWidth};});assert.ok(Object.values(chip).every(Boolean));
  await page.screenshot({path:join(dir,'usability-task-mobile.png')});
  await page.locator('#back-chain').click();assert.deepEqual(await ids('task'),chain);await page.locator('#back-overview').click();assert.equal(await ids('task').then(x=>x.length),0);assert.equal(await page.locator('.tg-history').isVisible(),false);
  checks.push('single-task replay isolates nodes and history; complete step labels and current status remain distinct');
  await page.locator('#activity-select').selectOption('unstarted');assert.ok(await page.locator('#empty').isVisible());await page.locator('#empty-reset').click();assert.equal(await ids('group').then(x=>x.length),4);checks.push('empty filter has a working reset without creating tasks');
  await page.setViewportSize({width:390,height:667});await page.waitForFunction(()=>document.querySelectorAll('.tg-node[data-kind=group]').length===2);await groupsOnPage();await chooseChain();await pause();await page.locator('.tg-history-step').last().click();await safeNode();
  await page.screenshot({path:join(dir,'usability-short-mobile.png')});
  const shortBar=await page.locator('.tg-history').boundingBox(),shortZoom=await page.locator('.tg-controls').boundingBox();assert.ok(shortBar.y+shortBar.height<=shortZoom.y);
  await page.locator('#back-overview').click();await page.setViewportSize({width:1440,height:800});await page.waitForFunction(()=>document.querySelectorAll('.tg-node[data-kind=group]').length===8);await groupsOnPage();
  checks.push('short 390x667 playback keeps current node clear; short desktop overview reduces density');
  await page.setViewportSize({width:1440,height:1000});await chooseChain();await pause();await page.locator('.tg-history-step').last().click();await safeNode();await page.screenshot({path:join(dir,'usability-chain-desktop.png')});
  await page.emulateMedia({reducedMotion:'reduce'});assert.match(await page.locator('[data-action=history-play]').textContent(),/播放/);const reduced=await current();await page.waitForTimeout(1500);assert.equal(await current(),reduced);assert.equal(await page.locator('.tg-node.has-history .tg-ring').first().evaluate(n=>getComputedStyle(n,'::before').animationName),'none');checks.push('reduced-motion stops autoplay and animation while keeping history available');
  // Explicit browser-only layout fixture: rewrite snapshot relations into a
  // 100-level chain. This tests app windowing/navigation, not server semantics.
  const deep=structuredClone(before),deepTasks=deep.tasks.filter(b=>b.application.project_id==='ux-100').sort((a,b)=>a.snapshot.task.task_id.localeCompare(b.snapshot.task.task_id));
  deep.tasks=deepTasks;deepTasks.forEach((b,index)=>{b.snapshot.task.relations=index?[{kind:'parent',target:{authority_id:config.authority,task_id:deepTasks[index-1].snapshot.task.task_id}}]:[];});
  await page.route('**/v1/queries',async route=>{if(route.request().postDataJSON().operation==='workspace.snapshot')await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({result:deep})});else await route.continue();});
  await page.reload();await page.locator('.connection.online').waitFor();await page.locator('#project-select').selectOption('ux-100');await groupsOnPage();assert.equal(await ids('group').then(x=>x.length),1);
  await page.locator('.tg-node[data-kind=group]').click();assert.equal(await ids('task').then(x=>x.length),24);assert.match(await page.locator('#project-caption').textContent(),/邻域 24\/100.*仅含当前可见任务/);
  const distant=deepTasks.at(-1).snapshot.task.task_id;
  await page.locator('#choose-focus').click();await page.locator('#focus-search').fill(distant);await page.locator('#focus-results button').click();
  const neighborhood=await ids('task');assert.equal(neighborhood.length,24);assert.ok(neighborhood.includes(distant));assert.ok(!neighborhood.includes(deepTasks[0].snapshot.task.task_id));
  await page.waitForFunction(id=>{const r=document.querySelector(`.tg-node[data-task-id="${id}"] .tg-orb`).getBoundingClientRect(),v=document.querySelector('.tg-viewport').getBoundingClientRect();return r.left>=v.left&&r.right<=v.right&&r.top>=v.top&&r.bottom<=v.bottom;},distant);
  checks.push('browser-only 100-level layout fixture bounds app to 24 tasks and search reaches distant neighborhood');
  await page.unroute('**/v1/queries');
  assert.equal(writes,0);assert.deepEqual(facts(await query('workspace.snapshot')),facts(before));assert.deepEqual(errors,[]);checks.push('all interactions are read-only; every task state/revision preserved; zero page errors');
  await writeFile(join(dir,'usability-results.json'),JSON.stringify({passed:true,url,checks,writes,errors,build:fixture.build||null,at:new Date().toISOString()},null,2),{mode:0o600});
  console.log(JSON.stringify({passed:true,checks,evidence:resolve(dir)},null,2));
}catch(error){await page.screenshot({path:join(dir,'usability-failure.png')});throw error;}
finally{await browser.close();}
