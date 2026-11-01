import test from 'node:test';
import assert from 'node:assert/strict';
import {groupTasks, groupNodes, focusNeighborhood} from './journeys.js';

const ref = (id, authority = 'local') => ({authority_id: authority, task_id: id});
const task = (id, relations = [], extra = {}) => ({id, ref: ref(id), title: id, state: 'offered', relations, ...extra});
const relation = (id, kind = 'parent', authority = 'local') => ({kind, target: ref(id, authority)});

test('same-ID foreign tasks, external links and absent targets never merge', () => {
  const tasks = [task('a'), task('a', [], {ref: ref('a', 'remote')}), task('b', [relation('a', 'parent', 'remote'), relation('a', 'external'), relation('missing')])];
  const groups = groupTasks(tasks, [{event_id: 'x', task_id: 'a'}]);
  assert.equal(groups.length, 3);
  assert.equal(new Set(groups.map(group => group.id)).size, 3);
  assert.ok(groups.every(group => group.lastActivity === -1));
  const explicit = groupTasks(tasks, [{task_id: 'a', authority_id: 'remote'}]);
  assert.equal(explicit[0].tasks[0].ref.authority_id, 'remote');
});
test('cycles and related/dependency edges group without inventing parent facts', () => {
  const tasks = [task('a', [relation('c')]), task('b', [relation('a', 'related')]), task('c', [relation('b', 'depends_on')])];
  const before = JSON.stringify(tasks);
  const groups = groupTasks(tasks);
  assert.equal(groups.length, 1);
  assert.equal(groups[0].anchorId, 'c');
  assert.equal(groups[0].counts.total, 3);
  assert.equal(JSON.stringify(tasks), before);
  assert.equal(groupTasks([...tasks].reverse())[0].id, groups[0].id);
});
test('independent tasks stay independent regardless of identical titles', () => {
  assert.equal(groupTasks([task('a', [], {title: 'same'}), task('b', [], {title: 'same'})]).length, 2);
});
test('execution facts precede recent events; event order beats timestamps and UUIDs', () => {
  const tasks = [task('a', [], {runs: [{}], state: 'completed'}), task('b'), task('c'), task('d', [], {generation: 1})];
  const groups = groupTasks(tasks, [{task_id: 'b', recorded_at: '2099', event_id: 'z'}, {task_id: 'c', recorded_at: '2000', event_id: 'a'}]);
  assert.deepEqual(groups.map(group => group.anchorId), ['a', 'd', 'c', 'b']);
  assert.match(groups[0].summaryText, /已完成1/);
  for (const field of ['assignments', 'runs', 'submissions']) assert.equal(groupTasks([task('x', [], {[field]: [{}]})])[0].hasExecution, true);
});
test('group node projection is explicitly a client view with no task relations', () => {
  const [node] = groupNodes(groupTasks([task('a'), task('b', [relation('a')])]));
  assert.equal(node.kind, 'group');
  assert.equal(node.ref.authority_id, 'ui-view');
  assert.equal(node.groupSize, 2);
  assert.deepEqual(node.relations, []);
  assert.deepEqual(node.memberIds, ['a', 'b']);
});
test('overview retains acceptance and failure counts ahead of completed and running work', () => {
  const states=['completed','active','submitted','failed','rejected'];
  const tasks=states.map((state,index)=>task(String(index),index?[relation('0')]:[],{state,generation:1}));
  const [node]=groupNodes(groupTasks(tasks));
  assert.equal(node.summaryText,'待验收 1 · 退回/失败 2');
  assert.match(node.expectedResult,/已完成1/);
  assert.match(node.expectedResult,/执行中1/);
  const [cancelled]=groupNodes(groupTasks([task('cancelled',[],{state:'cancelled'})]));
  assert.equal(cancelled.summaryText,'尚未承接 · 取消 1');
});
test('100-task neighborhood is bounded, contains focus and retains original order', () => {
  const tasks = Array.from({length: 100}, (_, index) => task(String(index), index ? [relation(String(index - 1))] : []));
  const result = focusNeighborhood(tasks, '50');
  assert.equal(result.tasks.length, 24);
  assert.equal(result.hiddenCount, 76);
  assert.ok(result.tasks.some(task => task.id === '50'));
  const numbers = result.tasks.map(task => Number(task.id));
  assert.deepEqual(numbers, [...numbers].sort((a, b) => a - b));
  assert.ok(Math.max(...numbers) - Math.min(...numbers) === 23);
  assert.deepEqual(focusNeighborhood(tasks.slice(0, 3), '2').tasks, tasks.slice(0, 3));
});
test('neighborhood uses only reachable same-authority input tasks and falls back to first', () => {
  const tasks = [task('a'), task('b', [relation('a')]), task('a', [], {ref: ref('a', 'remote')}), task('foreign', [relation('a', 'parent', 'remote')])];
  assert.deepEqual(focusNeighborhood(tasks, ref('a', 'remote'), 2).tasks.map(task => task.ref), [ref('a', 'remote')]);
  assert.deepEqual(focusNeighborhood(tasks, 'unknown', 2).tasks.map(task => task.id), ['a', 'b']);
});
