#!/usr/bin/env node
// Synthetic UI acceptance data, created only through the public protocol.
// Usage: node scripts/usability_fixture.mjs /absolute/isolated-fixture-directory
// Refuses an existing ux-100 project or intent directory. On failure preserve
// requests and inspect public facts; do not delete receipts to reseed blindly.
import {createHash, randomBytes} from 'node:crypto';
import {mkdir, open, readFile} from 'node:fs/promises';
import {isAbsolute, join, resolve} from 'node:path';
import {normalize} from '../web/model.js';
import {groupTasks} from '../web/journeys.js';

const binding = 'ledger-http-draft-1', protocol = 'task-core-draft-1';
const projectId = 'ux-100';
const states = ['offered', 'claimed', 'active', 'submitted', 'completed', 'rejected', 'failed', 'cancelled'];
const labels = {offered: '待领取', claimed: '已领取', active: '执行中', submitted: '待验收', completed: '已完成', rejected: '已拒绝', failed: '执行失败', cancelled: '已取消'};

async function save(path, value) {
  const file = await open(path, 'wx', 0o600);
  try { await file.writeFile(JSON.stringify(value, null, 2) + '\n'); await file.sync(); }
  finally { await file.close(); }
}

async function main() {
  const dir = process.argv[2];
  if (!dir || !isAbsolute(dir) || process.argv.length !== 3) throw new Error('Usage: node scripts/usability_fixture.mjs /absolute/isolated-fixture-directory');
  process.umask(0o077);
  const config = JSON.parse(await readFile(join(dir, 'codex-client.json'), 'utf8'));
  const server = JSON.parse(await readFile(join(dir, 'server.json'), 'utf8'));
  const url = new URL(config.service_url);
  if (url.protocol !== 'http:' || !['127.0.0.1', '[::1]'].includes(url.hostname) || url.username || url.password || url.pathname !== '/' || url.search || url.hash) throw new Error('Only an explicit loopback HTTP fixture is accepted; LAN services are forbidden.');
  if (server.listen !== url.host) throw new Error('Client URL and isolated server listen address differ.');
  const token = (await readFile(resolve(dir, config.credential_file), 'utf8')).trim();
  const output = join(dir, 'ux-100-fixture');
  let sequence = 0;
  let ready = false;

  async function request(route, payload, session = null, write = false) {
    const name = `${String(++sequence).padStart(4, '0')}-${payload.operation || payload.command.operation}`;
    if (write) await save(join(output, name + '.request.json'), {route: `/v1/${route}`, payload, session_id: session?.id || null});
    const headers = {'Content-Type': 'application/json', Authorization: `Bearer ${token}`};
    if (session) { headers['X-Ledger-Session'] = session.id; headers['X-Ledger-Session-Proof'] = session.secret; }
    const response = await fetch(`${url.origin}/v1/${route}`, {method: 'POST', headers, body: JSON.stringify(payload), redirect: 'error', signal: AbortSignal.timeout(10000)});
    const result = await response.json();
    if (ready) await save(join(output, name + '.response.json'), {status: response.status, result});
    if (!response.ok) throw new Error(`${name}: HTTP ${response.status}; original intent retained. Inspect public facts before retrying the exact request.`);
    return result.result;
  }
  const query = (operation, body = {}) => request('queries', {binding_version: binding, operation, body});
  const action = (id, operation, body, revision = 0, session = null) => request('actions', {binding_version: binding, request_id: `ux100-${id}`, operation, expected_revision: revision, body}, session, true);
  const info = await query('service.info');
  if (info.authority_id !== server.authority || info.protocol_version !== protocol || info.binding_version !== binding) throw new Error('Fixture authority or protocol mismatch.');
  const workspace = await query('workspace.snapshot');
  if (workspace.projects.some(project => project.project_id === projectId) || workspace.tasks.some(bundle => bundle.application.project_id === projectId)) throw new Error('ux-100 project already exists. Refusing duplicate or unknown writes; use saved receipts or a new isolated fixture.');
  await mkdir(output, {mode: 0o700});
  ready = true;
  await save(join(output, 'manifest.json'), {project_id: projectId, synthetic: true, purpose: '100-task UI acceptance fixture; no human UI approval and no actual Agent work simulated', authority_id: info.authority_id, expected_tasks: 100, expected_components: 20});
  await action('project', 'project.create', {project_id: projectId, title: '合成验收 · 百任务二十链', description: '仅用于隔离UI与协议验收。全部任务为合成数据；状态通过公开协议产生，不表示实际Agent正在执行，也不代表用户验收UI。'});
  const ref = taskId => ({authority_id: info.authority_id, task_id: taskId});
  const expected = {};
  for (let index = 0; index < 100; index++) {
    const chain = Math.floor(index / 5) + 1, step = index % 5 + 1;
    const taskId = `ux100-c${String(chain).padStart(2, '0')}-t${step}`;
    const state = states[index % states.length];
    expected[state] = (expected[state] || 0) + 1;
    let task = {revision: 0}, session = null;
    async function command(suffix, operation, body, application = {}) {
      const result = await request('commands', {binding_version: binding, command: {protocol_version: protocol, command_id: `${taskId}-${suffix}`, task: ref(taskId), expected_revision: task.revision, operation, body}, application}, session, true);
      task = result.task;
      return task;
    }
    const kind = step === 5 ? 'related' : 'parent';
    await command('create', 'task.create', {
      goal: `[合成] 链 ${String(chain).padStart(2, '0')} · 阶段 ${step} · ${labels[state]}`,
      expected_result: '仅验证任务列表、显式关联、状态及历史播放；不代表真实业务交付或人类批准。',
      ...(step > 1 ? {relations: [{kind, target: ref(`ux100-c${String(chain).padStart(2, '0')}-t${step - 1}`)}]} : {}),
      extensions: {'io.taskledger.fixture': {synthetic: true, purpose: '100-task UI acceptance', expected_state: state}, 'io.taskledger.ui': {kind: step === 5 ? 'variant' : 'child'}},
    }, {project_id: projectId, claim_policy: {mode: 'workspace'}});
    if (state === 'cancelled') await command('cancel', 'task.cancel', {reason: '合成验收：验证取消状态；不是用户取消真实任务。'});
    if (!['offered', 'cancelled'].includes(state)) {
      const bytes = randomBytes(32);
      session = {id: `${taskId}-session`, secret: bytes.toString('base64url')};
      await save(join(output, `${taskId}.session.json`), {session_id: session.id, secret: session.secret});
      await action(`${taskId}-begin`, 'session.begin', {session_id: session.id, project_id: projectId, goal: '合成状态链协议验收；不是实际Agent工作', proof_digest: createHash('sha256').update(bytes).digest('hex')});
      await command('claim', 'task.claim', {}, {session_id: session.id});
      if (state !== 'claimed') {
        const basis = task.expectation_version;
        await command('start', 'run.start', {assignment_id: task.current_assignment_id, generation: task.generation, basis_version: basis});
        const run = {assignment_id: task.current_assignment_id, generation: task.generation, run_id: task.current_run_id};
        await command('report', 'run.report', {...run, message: '合成验收轨迹：已通过公开协议领取并开始；验证历史阶段显示，不伪装实际业务实施。'});
        if (state === 'failed') await command('fail', 'run.fail', {...run, reason: '合成验收：显式失败路径。'});
        if (['submitted', 'completed', 'rejected'].includes(state)) {
          await command('submit', 'run.submit', {...run, basis_version: basis, summary: '合成验收交付：验证提交与验收轨迹，不代表实际业务完成。'});
          if (state !== 'submitted') await command('decide', 'task.decide', {submission_id: task.pending_submission_id, outcome: state === 'completed' ? 'accepted' : 'rejected', reason: '合成数据的Agent自验收；不是用户对UI的批准。'});
        }
      }
      const view = await query('session.get', {session_id: session.id});
      await action(`${taskId}-end`, 'session.end', {session_id: session.id, outcome: 'done', summary: '合成状态构造结束。若任务保留claimed/active，仅为静态UI验收状态，不存在持续执行进程。'}, view.session.revision, session);
    }
    if (task.state !== state) throw new Error(`${taskId}: expected ${state}, received ${task.state}`);
  }
  const final = await query('workspace.snapshot');
  const tasks = final.tasks.filter(bundle => bundle.application.project_id === projectId).map(normalize);
  const groups = groupTasks(tasks);
  const counts = tasks.reduce((result, task) => { result[task.state] = (result[task.state] || 0) + 1; return result; }, {});
  if (tasks.length !== 100 || groups.length !== 20 || groups.some(group => group.tasks.length !== 5) || states.some(state => counts[state] !== expected[state])) throw new Error('Final public snapshot failed count/component/state verification.');
  const summary = {project_id: projectId, synthetic: true, tasks: tasks.length, components: groups.length, tasks_per_component: 5, states: counts, ended_sessions: 100 - (counts.offered || 0) - (counts.cancelled || 0), note: 'All states are protocol facts from synthetic operations. Ended sessions do not release claimed/active tasks. No user UI approval.', receipts: output};
  await save(join(output, 'summary.json'), summary);
  console.log(JSON.stringify(summary, null, 2));
}

main().catch(error => { console.error(error.message); process.exitCode = 1; });
