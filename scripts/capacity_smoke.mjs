#!/usr/bin/env node
// Bounded mixed HTTP workload against the explicitly isolated frontend fixture.
import assert from 'node:assert/strict';
import {createHash, randomBytes} from 'node:crypto';
import {mkdir, open, readFile, realpath, writeFile} from 'node:fs/promises';
import {cpus, platform, release, totalmem} from 'node:os';
import {join, resolve} from 'node:path';
import {setTimeout as delay} from 'node:timers/promises';

const dir = process.argv[2] && await realpath(resolve(process.argv[2]));
const seconds = Number(process.argv[3] || 60);
assert.ok(dir && Number.isInteger(seconds) && seconds >= 10 && seconds <= 300,
  'Usage: node scripts/capacity_smoke.mjs /private/isolated-fixture-directory [10..300 seconds]');
process.umask(0o077);
const config = JSON.parse(await readFile(join(dir, 'server.json'), 'utf8'));
const fixture = JSON.parse(await readFile(join(dir, 'fixture.json'), 'utf8'));
assert.match(config.listen, /^127\.0\.0\.1:\d+$/);
assert.equal(config.authority, 'frontend-local');
const url = `http://${config.listen}`;
assert.equal(fixture.url, url);
assert.equal(await realpath(fixture.dir), dir);
const token = (await readFile(join(dir, 'owner-token'), 'utf8')).trim();
const intents = join(dir, 'capacity-intents');
await mkdir(intents, {mode: 0o700}); // A previous/partial run must not be silently reseeded.
const binding = 'ledger-http-draft-1', protocol = 'task-core-draft-1';
const project = 'capacity-smoke', metrics = {}, errors = [];
let stopping = false;
process.once('SIGINT', () => { stopping = true; });
process.once('SIGTERM', () => { stopping = true; });

async function request(route, body, {headers = {}, expected = 200, measure = false, replay = false} = {}) {
  const raw = JSON.stringify(body);
  if (route !== 'queries' && !replay) {
    const id = body.request_id || body.command.command_id;
    const file = await open(join(intents, `${id}.json`), 'wx', 0o600);
    try { await file.writeFile(raw); await file.sync(); } finally { await file.close(); }
  }
  const started = performance.now();
  const response = await fetch(`${url}/v1/${route}`, {
    method: 'POST', redirect: 'error', signal: AbortSignal.timeout(10000),
    headers: {'Content-Type': 'application/json', Authorization: `Bearer ${token}`, ...headers}, body: raw,
  });
  const text = await response.text(), value = JSON.parse(text);
  const operation = body.operation || body.command.operation;
  if (measure) {
    const samples = metrics[operation] ||= [];
    samples.push({ms: performance.now() - started, bytes: Buffer.byteLength(text), status: response.status});
  }
  assert.ok((Array.isArray(expected) ? expected : [expected]).includes(response.status),
    `${operation}: HTTP ${response.status}; ${JSON.stringify(value.error || {})}`);
  return {value, status: response.status};
}
const query = async (operation, body = {}, options = {}) =>
  (await request('queries', {binding_version: binding, operation, body}, options)).value.result;
const action = async (id, operation, body, revision = 0, headers = {}) =>
  (await request('actions', {binding_version: binding, request_id: id, operation, expected_revision: revision, body}, {headers})).value.result;
const envelope = (id, task, operation, revision, body, application = {}) => ({
  binding_version: binding,
  command: {protocol_version: protocol, command_id: id, task: {authority_id: config.authority, task_id: task},
    expected_revision: revision, operation, body}, application,
});

const startedAt = new Date().toISOString(), started = performance.now();
const results = {passed: false, started_at: startedAt, duration_seconds: seconds,
  build: fixture.build || null,
  workload: '4 paced workspace readers and 20 task creates with exact replays; one two-session claim race',
  environment: {platform: platform(), release: release(), cpu: cpus()[0]?.model, logical_cpus: cpus().length,
    memory_bytes: totalmem(), node: process.version}, transport: 'loopback HTTP, SQLite WAL/FULL',
  limits: 'Synthetic short workload, not a saturation test, long-duration soak, UI performance or production SLO.'};
try {
  const info = await query('service.info');
  assert.equal(info.authority_id, config.authority);
  assert.equal(info.protocol_version, protocol);
  const before = await query('workspace.snapshot');
  assert.equal(before.tasks.filter(b => b.application.project_id === 'ux-100').length, 100,
    'Run usability_fixture.mjs in this isolated fixture first');
  const originals = new Map(before.tasks.map(b => [b.snapshot.task.task_id, JSON.stringify(b)]));
  results.initial_tasks = before.tasks.length;
  await action('capacity-project', 'project.create', {project_id: project, title: 'Synthetic capacity smoke', description: 'Bounded release validation'});
  const raceID = 'capacity-race';
  await request('commands', envelope('capacity-race-create', raceID, 'task.create', 0,
    {goal: 'Synthetic concurrent claim', expected_result: 'Exactly one current assignment'},
    {project_id: project, claim_policy: {mode: 'workspace'}}));
  const sessions = [];
  for (let i = 0; i < 2; i++) {
    const secret = randomBytes(32), id = `capacity-session-${i}`;
    await action(`capacity-begin-${i}`, 'session.begin', {session_id: id, project_id: project,
      goal: 'Synthetic competing claim', proof_digest: createHash('sha256').update(secret).digest('hex')});
    sessions.push({id, headers: {'X-Ledger-Session': id, 'X-Ledger-Session-Proof': secret.toString('base64url')}});
  }
  // Each request records its original intent. Read status without assuming which contender wins.
  const raced = await Promise.allSettled(sessions.map(async (session, i) => {
    const payload = envelope(`capacity-claim-${i}`, raceID, 'task.claim', 1, {}, {session_id: session.id});
    return request('commands', payload, {headers: session.headers, expected: [200, 409]});
  }));
  assert.ok(raced.every(r => r.status === 'fulfilled'));
  assert.deepEqual(raced.map(r => r.value.status).sort(), [200, 409]);
  const task = (await query('task.snapshot', {task: {authority_id: config.authority, task_id: raceID}})).bundle.snapshot.task;
  assert.equal(task.state, 'claimed'); assert.equal(task.generation, 1);
  await request('commands', envelope('capacity-release', raceID, 'assignment.release', task.revision,
    {assignment_id: task.current_assignment_id, generation: task.generation, reason: 'Synthetic claim check complete'}));
  for (const session of sessions) {
    const state = await query('session.get', {session_id: session.id});
    assert.equal(state.current_binding, null);
    await action(`capacity-end-${session.id}`, 'session.end', {session_id: session.id, outcome: 'done',
      summary: 'Synthetic competing claim complete'}, state.session.revision, session.headers);
  }
  results.single_assignment_race = true;
  const deadline = performance.now() + seconds * 1000;
  const read = async () => {
    while (!stopping && performance.now() < deadline) {
      const snapshot = await query('workspace.snapshot', {}, {measure: true});
      assert.equal(new Set(snapshot.tasks.map(b => b.snapshot.task.task_id)).size, snapshot.tasks.length);
      await delay(100);
    }
  };
  const write = async () => {
    for (let i = 0; i < 20 && !stopping; i++) {
      const payload = envelope(`capacity-create-${i}`, `capacity-task-${i}`, 'task.create', 0,
        {goal: `Synthetic capacity task ${i}`, expected_result: 'One durable task despite an exact retry'},
        {project_id: project, claim_policy: {mode: 'workspace'}});
      const first = await request('commands', payload, {measure: true});
      const replay = await request('commands', payload, {measure: true, replay: true});
      assert.equal(first.value.result.replayed, false);
      assert.equal(replay.value.result.replayed, true);
      assert.deepEqual({...replay.value.result, replayed: false}, first.value.result);
      await delay(250);
    }
  };
  const guarded = async operation => {
    try { return await operation(); }
    catch (error) { stopping = true; throw error; }
  };
  const operations = await Promise.allSettled([guarded(read), guarded(read), guarded(read), guarded(read), guarded(write)]);
  for (const operation of operations) if (operation.status === 'rejected') errors.push(String(operation.reason));
  assert.deepEqual(errors, []); assert.equal(stopping, false, 'Run interrupted');
  const after = await query('workspace.snapshot');
  assert.equal(after.tasks.filter(b => b.application.project_id === project).length, 21);
  for (const bundle of after.tasks) {
    const previous = originals.get(bundle.snapshot.task.task_id);
    if (previous !== undefined) assert.equal(JSON.stringify(bundle), previous, 'Original task changed');
  }
  assert.equal(after.tasks.length, before.tasks.length + 21);
  results.final_tasks = after.tasks.length;
  results.idempotent_creates = 20; results.original_tasks_unchanged = true; results.passed = true;
} catch (error) {
  results.error = String(error); process.exitCode = 1;
} finally {
  results.elapsed_seconds = (performance.now() - started) / 1000;
  results.metrics = Object.fromEntries(Object.entries(metrics).map(([name, samples]) => {
    const times = samples.map(s => s.ms).sort((a, b) => a - b);
    const at = percentile => times[Math.max(0, Math.ceil(times.length * percentile) - 1)];
    return [name, {requests: times.length, p50_ms: at(.5), p95_ms: at(.95), max_ms: times.at(-1),
      max_response_bytes: Math.max(...samples.map(s => s.bytes)), errors: samples.filter(s => s.status !== 200).length}];
  }));
  await writeFile(join(dir, 'capacity-results.json'), JSON.stringify(results, null, 2) + '\n', {mode: 0o600});
  console.log(JSON.stringify(results, null, 2));
}
