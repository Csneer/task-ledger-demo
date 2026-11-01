import test from 'node:test';
import assert from 'node:assert/strict';
import {layoutTasks, layoutGroups, fitTransform, visibleHistory, historyRound, TaskGraph} from './graph.js';

const ref = (id, authority = 'local') => ({authority_id: authority, task_id: id});
const task = (id, parent, more = {}) => ({id, ref: ref(id), title: id, relations: parent ? [{kind: 'parent', task: ref(parent)}] : [], ...more});

test('history rounds distinguish pre-claim from numbered assignments without guessing missing facts', () => {
  assert.equal(historyRound(0), '领取前');
  assert.equal(historyRound(1), '第 1 轮承接');
  assert.equal(historyRound(12), '第 12 轮承接');
  for (const value of [null, undefined, -1, '1']) assert.equal(historyRound(value), '');
});

test('group overview uses four desktop or two mobile columns without scaling labels', () => {
  const groups = Array.from({length: 12}, (_, i) => task(String(i), null, {kind: 'group'}));
  const desktop = layoutGroups(groups, 1440, 900), mobile = layoutGroups(groups.slice(0, 4), 390, 700);
  assert.equal(desktop.columns, 4);
  assert.equal(mobile.columns, 2);
  for (const layout of [desktop, mobile]) {
    const rows = new Set([...layout.nodes.values()].map(node => node.y));
    assert.equal(rows.size, layout === desktop ? 3 : 2);
    for (const node of layout.nodes.values()) assert.ok(node.x > 80 && node.x < layout.width - 80);
  }
});

test('history preserves authority order, deduplicates and filters only invisible tasks', () => {
  const events = [{event_id: 'z', task_id: 'child', recorded_at: 'later'}, {event_id: 'a', task_id: 'parent'}, {event_id: 'hidden', task_id: 'outside'}, {event_id: 'z', task_id: 'child'}];
  assert.deepEqual(visibleHistory(events, new Set(['parent', 'child'])), events.slice(0, 2));
});
test('history refresh preserves paused position and clock on polling and appends', () => {
  const events = [{event_id: '1', task_id: 'a'}, {event_id: '2', task_id: 'b'}];
  const graph = {tasks: [task('a'), task('b')], history: events, historySource: [...events, {event_id: '3', task_id: 'a'}], historyIndex: 1, historyElapsed: 1200, historyPlaying: false, historyInitialized: true, renderHistory() {}};
  TaskGraph.prototype.refreshHistory.call(graph);
  assert.equal(graph.historyIndex, 1);
  assert.equal(graph.historyElapsed, 1200);
  assert.equal(graph.historyPlaying, false);
  assert.equal(graph.history.length, 3);
});
test('client-only group nodes never acquire task history or replay authority', () => {
  const graph = {tasks: [task('group:one', null, {kind: 'group'})], historySource: [{event_id: '1', task_id: 'group:one'}], history: [], historyIndex: 0, historyInitialized: true, renderHistory() {}};
  TaskGraph.prototype.refreshHistory.call(graph);
  assert.deepEqual(graph.history, []);
  assert.equal(TaskGraph.prototype.replayTask.call(graph, 'group:one'), false);
});
test('history advances in supplied order, loops, and cannot advance while paused', () => {
  const graph = {history: [{event_id: 'z'}, {event_id: 'a'}], historyIndex: 0, historyLastTime: 0, historyElapsed: 2300, historyPlaying: true, selectHistory(index) { this.historyIndex = index; this.historyElapsed = 0; }};
  TaskGraph.prototype.advanceHistory.call(graph, 100);
  assert.equal(graph.historyIndex, 1);
  graph.historyElapsed = 2300;
  TaskGraph.prototype.advanceHistory.call(graph, 200);
  assert.equal(graph.historyIndex, 0);
  graph.historyPlaying = false;
  graph.historyElapsed = 2300;
  TaskGraph.prototype.advanceHistory.call(graph, 300);
  assert.equal(graph.historyElapsed, 2300);
});
test('task replay isolates one visible task, starts at first event and honors reduced motion', () => {
  const graph = {tasks: [task('a'), task('b')], historySource: [{event_id: '1', task_id: 'b'}, {event_id: '2', task_id: 'a'}, {event_id: '3', task_id: 'a'}], history: [], historyIndex: 0, historyInitialized: true, motion: {matches: true}, renderHistory() {}, selectHistory(index) { this.historyIndex = index; }, focus(id) { this.focused = id; }};
  graph.refreshHistory = () => TaskGraph.prototype.refreshHistory.call(graph);
  assert.equal(TaskGraph.prototype.replayTask.call(graph, 'missing'), false);
  assert.equal(TaskGraph.prototype.replayTask.call(graph, 'a'), true);
  assert.deepEqual(graph.history.map(event => event.event_id), ['2', '3']);
  assert.equal(graph.historyIndex, 0);
  assert.equal(graph.historyPlaying, false);
  assert.equal(graph.focused, undefined, 'replay does not steal DOM focus');
  graph.tasks = [task('b')];
  graph.refreshHistory();
  assert.equal(graph.historyTaskId, null);
  assert.deepEqual(graph.history.map(event => event.event_id), ['1']);
});
test('history pulse bypasses live deduplication without mutating live seen ids or task state', () => {
  const graph = {seen: new Set(['existing']), buttons: new Map()};
  TaskGraph.prototype.pulse.call(graph, {event_id: 'historical', task_id: 'a'}, {historical: true});
  assert.deepEqual([...graph.seen], ['existing']);
});

test('explicit deep parent chains lay out in increasing columns', () => {
  const layout = layoutTasks([task('a'), task('b', 'a'), task('c', 'b')]);
  assert.ok(layout.nodes.get('a').x < layout.nodes.get('b').x);
  assert.ok(layout.nodes.get('b').x < layout.nodes.get('c').x);
  assert.equal(layout.edges.length, 2);
});
test('cycle is represented but cannot recurse forever or lose nodes', () => {
  const layout = layoutTasks([task('a', 'c'), task('b', 'a'), task('c', 'b')]);
  assert.equal(layout.nodes.size, 3);
  assert.equal(layout.edges.length, 3);
  for (const node of layout.nodes.values()) assert.ok(Number.isFinite(node.x) && Number.isFinite(node.y));
});
test('foreign authority and missing tasks remain external references', () => {
  const layout = layoutTasks([task('a'), task('b', null, {relations: [{kind: 'parent', task: ref('a', 'remote')}, {kind: 'related', task: ref('missing')}]})]);
  assert.equal(layout.edges.length, 0);
  assert.equal(layout.external.length, 2);
  assert.equal(layout.nodes.get('b').parent, null);
});
test('related attempts do not acquire invented parent or dependency semantics', () => {
  const layout = layoutTasks([task('a'), task('b', null, {branchKind: 'variant', relations: [{kind: 'related', task: ref('a')}]})]);
  assert.equal(layout.nodes.get('b').parent, null);
  assert.equal(layout.edges[0].branchKind, 'variant');
});
test('independent 30-task workspace has no overlapping node positions', () => {
  const layout = layoutTasks(Array.from({length: 30}, (_, i) => task(String(i))));
  const positions = new Set([...layout.nodes.values()].map(n => `${n.x},${n.y}`));
  assert.equal(positions.size, 30);
});
test('20-level and maximum 1000-level chains fully fit without a fixed lower scale', () => {
  for (const count of [20, 1000]) {
    const layout = layoutTasks(Array.from({length: count}, (_, i) => task(String(i), i ? String(i - 1) : null)));
    assert.equal(layout.nodes.size, count);
    for (const [width, height] of [[1000, 600], [390, 600]]) {
      const view = fitTransform(layout, width, height);
      assert.ok(view.scale > 0 && view.scale < .35);
      assert.ok(view.offset.x >= 0 && view.offset.y >= 0);
      assert.ok(view.offset.x + layout.width * view.scale <= width);
      assert.ok(view.offset.y + layout.height * view.scale <= height);
    }
  }
});
test('zooming out from a deep-chain fit does not jump up to the old minimum', () => {
  const layout = layoutTasks(Array.from({length: 20}, (_, i) => task(String(i), i ? String(i - 1) : null)));
  const view = fitTransform(layout, 1000, 600);
  const graph = {layout, viewport: {clientWidth: 1000, clientHeight: 600}, ...view, transform() {}};
  TaskGraph.prototype.zoom.call(graph, .8);
  assert.equal(graph.scale, view.scale);
  TaskGraph.prototype.zoom.call(graph, 1.2);
  assert.ok(graph.scale > view.scale);
  TaskGraph.prototype.zoom.call(graph, 1000);
  assert.equal(graph.scale, 2);
});
test('focus restores readable node scale after whole-graph overview', () => {
  const layout = layoutTasks(Array.from({length: 1000}, (_, i) => task(String(i), i ? String(i - 1) : null)));
  let focused = false;
  const graph = {layout, viewport: {clientWidth: 1000, clientHeight: 600}, ...fitTransform(layout, 1000, 600), buttons: new Map([['999', {focus() { focused = true; }}]]), transform() {}};
  TaskGraph.prototype.focus.call(graph, '999');
  assert.equal(graph.scale, .9);
  assert.equal(graph.offset.x + layout.nodes.get('999').x * graph.scale, 500);
  assert.equal(focused, true);
});
test('viewport resize preserves centered world point and zoom without refitting or focusing', () => {
  const graph = {viewport: {clientWidth: 390, clientHeight: 700}, viewportSize: {width: 1440, height: 900}, fitted: true, scale: .9, offset: {x: 180, y: 90}, transform() {}, fit() { assert.fail('already fitted'); }};
  const center = {x: (720 - graph.offset.x) / graph.scale, y: (450 - graph.offset.y) / graph.scale};
  TaskGraph.prototype.resizeViewport.call(graph);
  assert.equal(center.x * graph.scale + graph.offset.x, 195);
  assert.equal(center.y * graph.scale + graph.offset.y, 350);
  assert.equal(graph.scale, .9);
  graph.viewport.clientWidth = 0;
  TaskGraph.prototype.resizeViewport.call(graph);
  assert.deepEqual(graph.viewportSize, {width: 390, height: 700});
});
test('suspended playback preserves play intent and elapsed progress', () => {
  const graph = {historyPlaying: true, history: [{}], historyLastTime: 0, historyElapsed: 1000, playbackBlocked: () => true};
  TaskGraph.prototype.advanceHistory.call(graph, 100);
  assert.equal(graph.historyElapsed, 1000);
  assert.equal(graph.historyPlaying, true);
  TaskGraph.prototype.setPlaybackSuspended.call(graph, true);
  assert.equal(graph.playbackSuspended, true);
  assert.equal(graph.historyLastTime, null);
  assert.equal(graph.historyPlaying, true);
});
test('camera follows to readable scale without focusing and is suppressed by manual navigation', () => {
  const graph = {historyFollow: true, historyPlaying: true, history: [{task_id: 'a'}], historyIndex: 0, layout: {nodes: new Map([['a', {x: 900, y: 500}]])}, scale: .2, offset: {x: 0, y: 0}, playbackBlocked: () => false, readingCenter: () => ({x: 195, y: 280}), motion: {matches: true}, transform() {}, renderHistory() {}};
  TaskGraph.prototype.followHistory.call(graph);
  assert.equal(graph.scale, .9);
  assert.equal(900 * graph.scale + graph.offset.x, 195);
  assert.equal(500 * graph.scale + graph.offset.y, 280);
  TaskGraph.prototype.pauseFollow.call(graph);
  graph.offset.x = 42;
  TaskGraph.prototype.followHistory.call(graph);
  assert.equal(graph.offset.x, 42);
  assert.equal(graph.historyPlaying, true);
});
test('short-screen reading center respects real player edge and asymmetric node labels', () => {
  const graph = {viewport: {clientWidth: 390, clientHeight: 386}, historyBar: {hidden: false, offsetTop: 199}, scale: .9, history: [{task_id: 'a'}], historyIndex: 0, buttons: new Map([['a', {offsetHeight: 127, querySelector: () => ({offsetHeight: 26})}]])};
  const center = TaskGraph.prototype.readingCenter.call(graph);
  const above = 127 * .9 / 2 + 24 * .9, below = 127 * .9 / 2 + 35 * .9;
  assert.equal(center.x, 195);
  assert.ok(center.y - above >= 8);
  assert.ok(center.y + below <= 199 - 16);
});
test('overview fit cancels a previous task playback camera before its next frame', () => {
  const tasks = [task('group:a', null, {kind: 'group'})];
  const graph = {tasks, layout: layoutGroups(tasks, 390, 386), groupView: true, viewport: {clientWidth: 390, clientHeight: 386}, camera: {started: 0, from: {x: 0, y: 0}, fromScale: .9, target: {x: -900, y: -500}, scale: 1.1}, transform() {}};
  TaskGraph.prototype.fit.call(graph);
  const fitted = {...graph.offset};
  assert.equal(graph.camera, null);
  TaskGraph.prototype.advanceCamera.call(graph, 700);
  assert.deepEqual(graph.offset, fitted);
  assert.equal(graph.scale, 1);
});
test('manual focus cancels a previous playback camera before its next frame', () => {
  const graph = {layout: layoutTasks([task('a')]), viewport: {clientWidth: 390, clientHeight: 500}, scale: .9, buttons: new Map(), camera: {started: 0}, transform() {}};
  TaskGraph.prototype.focus.call(graph, 'a');
  const focused = {...graph.offset};
  assert.equal(graph.camera, null);
  TaskGraph.prototype.advanceCamera.call(graph, 700);
  assert.deepEqual(graph.offset, focused);
});
