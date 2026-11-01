#!/usr/bin/env node
// Opt-in local UI/Agent fixture. The daemon is a direct child; no Agent is launched.
import {execFile, spawn} from 'node:child_process';
import {createHash, randomBytes, randomUUID} from 'node:crypto';
import {chmod, lstat, mkdir, open, readFile, readdir, realpath} from 'node:fs/promises';
import {createServer} from 'node:net';
import {isAbsolute, join, resolve} from 'node:path';
import {setTimeout as delay} from 'node:timers/promises';
import {promisify} from 'node:util';

const binding = 'ledger-http-draft-1';
const protocol = 'task-core-draft-1';
const authority = 'frontend-local';
const projectId = 'frontend';
const parentTaskId = 'frontend-build';
const agentTaskId = 'frontend-review';
const owner = {kind: 'human', id: 'owner'};
const codex = {kind: 'agent', id: 'codex'};
let daemonProcess = null;
let daemonExit = null;
let daemonLog = null;
let stopping = false;

function argumentsFor(argv) {
  const args = {port: 0};
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--help' || argv[i] === '-h') return {help: true};
    const name = argv[i];
    if (!['--daemon', '--dir', '--port'].includes(name) || i + 1 === argv.length || argv[i + 1].startsWith('--')) throw new Error('usage: node scripts/frontend_fixture.mjs --daemon /path/taskledgerd --dir /private/new-directory [--port 8090]');
    if (name === '--port') {
      const value = argv[++i];
      if (!/^\d+$/.test(value) || Number(value) < 0 || Number(value) > 65535) throw new Error('--port must be an integer from 0 through 65535');
      args.port = Number(value);
    } else args[name.slice(2)] = argv[++i];
  }
  if (!args.daemon || !args.dir) throw new Error('--daemon and --dir are required; use --help for usage');
  if (!isAbsolute(args.dir)) throw new Error('--dir must be an absolute path to a new or empty private directory');
  return args;
}

async function privateWrite(path, value) {
  const file = await open(path, 'wx', 0o600);
  try { await file.writeFile(typeof value === 'string' ? value : JSON.stringify(value, null, 2) + '\n'); await file.sync(); }
  finally { await file.close(); }
}

async function prepareDirectory(path) {
  const requested = resolve(path);
  if (requested === '/') throw new Error('a dedicated fixture directory is required');
  try {
    const info = await lstat(requested);
    if (!info.isDirectory() || info.isSymbolicLink()) throw new Error('--dir must be a real directory, not a symlink');
    if ((await readdir(requested)).length) throw new Error('--dir is not empty; existing data will not be overwritten. Resume its daemon directly or choose a new directory.');
  } catch (error) {
    if (error.code !== 'ENOENT') throw error;
    await mkdir(requested, {mode: 0o700});
  }
  await chmod(requested, 0o700);
  const dir = await realpath(requested);
  await mkdir(join(dir, 'intents'), {mode: 0o700});
  await mkdir(join(dir, 'codex-intents'), {mode: 0o700});
  return dir;
}

async function unusedPort(port) {
  const server = createServer();
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(port, '127.0.0.1', resolve); });
  const selected = server.address().port;
  await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
  return selected;
}

function startDaemon(executable, config, initialize) {
  daemonProcess = spawn(executable, ['-config', config, ...(initialize ? ['-init'] : [])], {
    stdio: ['ignore', 'ignore', daemonLog.fd],
    shell: false,
  });
  const child = daemonProcess;
  daemonExit = new Promise(resolve => {
    child.once('error', error => resolve({error}));
    child.once('close', (code, signal) => resolve({code, signal}));
  });
  return daemonExit;
}

async function stopDaemon() {
  if (!daemonProcess || daemonProcess.exitCode !== null || daemonProcess.signalCode !== null) return;
  const child = daemonProcess;
  child.kill('SIGTERM');
  const finished = await Promise.race([daemonExit.then(() => true), delay(10500, false, {ref: false})]);
  if (!finished) { child.kill('SIGKILL'); await daemonExit; }
}

async function main() {
  const args = argumentsFor(process.argv.slice(2));
  if (args.help) {
    console.log('Usage: node scripts/frontend_fixture.mjs --daemon /path/taskledgerd --dir /private/new-directory [--port 8090]\nCreates private credentials and an isolated database; seeds offered frontend tasks using public HTTP. Keeps its daemon running until SIGINT/SIGTERM. Existing nonempty directories are never overwritten. No Agent, report or acceptance is simulated.');
    return;
  }
  process.umask(0o077);
  const executable = await realpath(resolve(args.daemon));
  const executableInfo = await lstat(executable);
  if (!executableInfo.isFile() || !(executableInfo.mode & 0o111)) throw new Error('--daemon must be an executable file');
  const build = {identity: (await promisify(execFile)(executable, ['-version'], {timeout: 5000})).stdout.trim(),
    sha256: createHash('sha256').update(await readFile(executable)).digest('hex')};
  const dir = await prepareDirectory(args.dir);
  const port = await unusedPort(args.port);
  const url = `http://127.0.0.1:${port}`;
  const ownerBytes = randomBytes(32), codexBytes = randomBytes(32);
  const ownerToken = ownerBytes.toString('base64url');
  const ownerTokenFile = join(dir, 'owner-token');
  const codexTokenFile = join(dir, 'codex-token');
  const codexConfig = join(dir, 'codex-client.json');
  const serverConfig = join(dir, 'server.json');
  await privateWrite(ownerTokenFile, ownerToken + '\n');
  await privateWrite(codexTokenFile, codexBytes.toString('base64url') + '\n');
  await privateWrite(codexConfig, {service_url: url, credential_file: 'codex-token', intent_dir: 'codex-intents'});
  await privateWrite(join(dir, 'owner-client.json'), {service_url: url, credential_file: 'owner-token', intent_dir: 'intents'});
  await privateWrite(serverConfig, {
    authority,
    database: 'ledger.db',
    listen: `127.0.0.1:${port}`,
    operators: [owner],
    credentials: [
      {digest: createHash('sha256').update(ownerBytes).digest('hex'), actor: owner, read: true, write: true, admin: true},
      {digest: createHash('sha256').update(codexBytes).digest('hex'), actor: codex, read: true, write: true, admin: false},
    ],
  });
  daemonLog = await open(join(dir, 'daemon.log'), 'ax', 0o600);
  if (stopping) return;
  const initialized = await startDaemon(executable, serverConfig, true);
  if (initialized.error || initialized.code !== 0) throw new Error(`daemon initialization failed; see ${join(dir, 'daemon.log')}`);
  if (stopping) return;
  startDaemon(executable, serverConfig, false);

  async function request(route, payload, write = false) {
    if (write) await privateWrite(join(dir, 'intents', `${payload.request_id || payload.command.command_id}.json`), payload);
    const response = await fetch(`${url}/v1/${route}`, {
      method: 'POST', redirect: 'error', signal: AbortSignal.timeout(5000),
      headers: {'Content-Type': 'application/json', Authorization: `Bearer ${ownerToken}`},
      body: JSON.stringify(payload),
    });
    const result = await response.json();
    if (!response.ok) throw new Error(`public API ${route}: HTTP ${response.status} ${result.error?.code || result.error?.error?.kind || 'INVALID_RESPONSE'}. Original intent retained; do not reseed this directory.`);
    return result;
  }

  let ready = false;
  for (let attempt = 0; attempt < 80; attempt++) {
    if (stopping) return;
    if (daemonProcess.exitCode !== null || daemonProcess.signalCode !== null) throw new Error(`daemon exited before ready; see ${join(dir, 'daemon.log')}`);
    try {
      const result = await request('queries', {binding_version: binding, operation: 'service.info', body: {}});
      if (result.result.authority_id !== authority || result.result.protocol_version !== protocol) throw new Error('unexpected service identity');
      ready = true;
      break;
    } catch (error) {
      if (!['ECONNREFUSED', 'UND_ERR_CONNECT_TIMEOUT'].includes(error.cause?.code) && error.name !== 'TimeoutError' && !error.message.includes('fetch failed')) throw error;
      await delay(150);
    }
  }
  if (!ready) throw new Error(`daemon readiness timed out; see ${join(dir, 'daemon.log')}`);
  const action = (operation, body) => request('actions', {binding_version: binding, request_id: randomUUID(), operation, expected_revision: 0, body}, true);
  await action('participant.create', {actor: codex, display_name: '本机 Codex', state: 'enabled', profile: {provider: 'codex', capabilities: ['frontend-review', 'implementation']}});
  await action('project.create', {project_id: projectId, title: '任务星轨 · 前端开发', description: '用当前前端开发验证真实任务发布、Agent 领取、交付与人工验收。初始任务均待领取；示例分支尚未执行。'});

  const ref = id => ({authority_id: authority, task_id: id});
  const tasks = [
    {id: parentTaskId, goal: '实现任务星轨前端', expected: '交付接入真实后端的任务节点画布，支持发布、派生子任务、交付验收与事件变化；提供实际验证证据。'},
    {id: agentTaskId, goal: '前端交互与协作约定验收', expected: '由本机 Codex 主动领取，检查任务画布和协作 Skill 的真实实现，记录可复验的命令与结果、发现的问题及限制，提交验收报告。只检查明确指定的前端与 Skill 范围，不自动提交或推送代码。', parent: parentTaskId, target: true},
    {id: 'frontend-tooltip-review', goal: '验证节点详情与悬停反馈', expected: '检查节点选择、tooltip、详情弹窗与键盘可达性，报告实际观察；此任务当前仅发布，尚未执行。', parent: agentTaskId, synthetic: true},
    {id: 'frontend-motion-a', goal: '任务流转动效 · 方案 A', expected: '比较节点光环与路径反馈如何表达提交和验收，提交方案及验证截图。合成开发样例，未执行。', related: parentTaskId, variant: 1, synthetic: true},
    {id: 'frontend-motion-b', goal: '任务流转动效 · 方案 B', expected: '比较不同分支动效与并行任务识别方式，提交方案及验证截图。合成开发样例，未执行。', related: parentTaskId, variant: 2, synthetic: true},
  ];
  for (const task of tasks) {
    if (stopping) return;
    const body = {goal: task.goal, expected_result: task.expected};
    if (task.parent) body.relations = [{kind: 'parent', target: ref(task.parent)}];
    if (task.related) body.relations = [{kind: 'related', target: ref(task.related)}];
    if (task.parent || task.variant || task.synthetic) body.extensions = {
      'io.taskledger.ui': {kind: task.variant ? 'variant' : 'child', ...(task.variant ? {group_id: 'frontend-motion-options', variant_index: task.variant} : {})},
      'io.taskledger.fixture': {synthetic: !!task.synthetic, purpose: task.synthetic ? 'offered-only UI exploration' : 'real frontend development acceptance'},
    };
    await request('commands', {
      binding_version: binding,
      command: {protocol_version: protocol, command_id: randomUUID(), task: ref(task.id), expected_revision: 0, operation: 'task.create', body},
      application: {project_id: projectId, claim_policy: task.target ? {mode: 'target', target: codex} : {mode: 'workspace'}},
    }, true);
  }
  const snapshot = await request('queries', {binding_version: binding, operation: 'workspace.snapshot', body: {}});
  if (snapshot.result.tasks.length !== tasks.length || snapshot.result.tasks.some(bundle => bundle.snapshot.task.state !== 'offered')) throw new Error('fixture seed verification failed: expected only offered tasks');
  const manifest = {url, dir, ownerTokenFile, codexConfig, projectId, parentTaskId, agentTaskId, authorityId: authority, codexTokenFile, serverConfig, build, taskIds: tasks.map(task => task.id)};
  await privateWrite(join(dir, 'fixture.json'), manifest);
  // stdout is a machine protocol: this is its first line and contains no secrets.
  console.log(JSON.stringify(manifest));
  console.error(`[fixture] ready at ${url}; SIGINT/SIGTERM stops only its daemon. Private data retained at ${dir}`);
  const exit = await daemonExit;
  if (!stopping) throw new Error(`daemon stopped unexpectedly (${exit.error?.message || exit.code || exit.signal}); private data retained at ${dir}`);
}

for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => {
  if (stopping) return;
  stopping = true;
  stopDaemon().then(() => { process.exitCode = 0; });
});

try { await main(); }
catch (error) { if (!stopping) { console.error(`[fixture] ${error.message}`); process.exitCode = 1; } }
finally { await stopDaemon(); if (daemonLog) await daemonLog.close(); }
