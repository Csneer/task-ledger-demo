// View-only projections. Connections do not imply parenthood or state propagation.
const key = ref => JSON.stringify([ref?.authority_id || '', ref?.task_id || '']);
const compare = (a, b) => a < b ? -1 : a > b ? 1 : 0;
const kinds = new Set(['parent', 'related', 'depends_on']);
const labels = {offered: '待领取', claimed: '已领取', active: '执行中', submitted: '待验收', completed: '已完成', rejected: '已拒绝', failed: '执行失败', cancelled: '已取消'};

function connections(tasks) {
  const byRef = new Map(tasks.map(task => [key(task.ref), task]));
  const neighbors = new Map([...byRef.keys()].map(id => [id, new Set()]));
  const outbound = new Set();
  for (const task of tasks) {
    const from = key(task.ref);
    for (const relation of task.relations || []) {
      const target = relation.target || relation.task;
      if (!kinds.has(relation.kind) || target?.authority_id !== task.ref?.authority_id) continue;
      const to = key(target);
      if (!byRef.has(to) || from === to) continue;
      neighbors.get(from).add(to);
      neighbors.get(to).add(from);
      if (relation.kind === 'parent' || relation.kind === 'related') outbound.add(from);
    }
  }
  return {byRef, neighbors, outbound};
}

function execution(task) {
  return Number(task.generation) > 0 || Boolean(task.assignment || task.run || task.submission) || ['assignments', 'runs', 'submissions'].some(field => task[field]?.length > 0);
}

export function groupTasks(tasks, events = []) {
  const {byRef, neighbors, outbound} = connections(tasks);
  const ids = new Map();
  for (const task of tasks) { const list = ids.get(task.id) || []; list.push(key(task.ref)); ids.set(task.id, list); }
  const activity = new Map();
  events.forEach((event, index) => {
    const ref = event.task || event.ref;
    const taskId = event.task_id || ref?.task_id;
    const authority = event.authority_id || ref?.authority_id;
    const id = authority ? key({authority_id: authority, task_id: taskId}) : ids.get(taskId)?.length === 1 ? ids.get(taskId)[0] : null;
    if (id && byRef.has(id)) activity.set(id, index);
  });
  const visited = new Set(), groups = [];
  for (const start of byRef.keys()) {
    if (visited.has(start)) continue;
    const pending = [start], members = new Set([start]);
    visited.add(start);
    for (let index = 0; index < pending.length; index++) {
      for (const neighbor of neighbors.get(pending[index])) {
        if (visited.has(neighbor)) continue;
        visited.add(neighbor); members.add(neighbor); pending.push(neighbor);
      }
    }
    const candidates = [...members].filter(id => !outbound.has(id)).sort(compare);
    const anchorKey = candidates[0] || [...members].sort(compare)[0];
    const anchor = byRef.get(anchorKey);
    const selected = tasks.filter(task => members.has(key(task.ref)));
    const counts = {total: selected.length, ...Object.fromEntries(Object.keys(labels).map(state => [state, 0]))};
    for (const task of selected) counts[task.state] = (counts[task.state] || 0) + 1;
    const summary = Object.entries(labels).filter(([state]) => counts[state]).map(([state, label]) => `${label}${counts[state]}`);
    for (const state of Object.keys(counts).filter(state => state !== 'total' && !labels[state]).sort(compare)) summary.push(`${state}${counts[state]}`);
    groups.push({id: 'group:' + encodeURIComponent(anchorKey), anchorId: anchor.id, title: anchor.title || anchor.goal || anchor.id, memberIds: selected.map(task => task.id), tasks: selected, counts, hasExecution: selected.some(execution), lastActivity: Math.max(-1, ...[...members].map(id => activity.get(id) ?? -1)), summaryText: `${selected.length} 项${summary.length ? ' · ' + summary.join(' · ') : ''}`});
  }
  return groups.sort((a, b) => Number(b.hasExecution) - Number(a.hasExecution) || b.lastActivity - a.lastActivity || compare(a.id, b.id));
}

export function groupNodes(groups) {
  return groups.map(group => {
    const c=group.counts, parts=[];
    if(c.submitted)parts.push(`待验收 ${c.submitted}`);
    if(c.rejected+c.failed)parts.push(`退回/失败 ${c.rejected+c.failed}`);
    if(c.claimed+c.active)parts.push(`实施 ${c.claimed+c.active}`);
    if(c.completed)parts.push(`完成 ${c.completed}`);
    if(c.offered)parts.push(`待领取 ${c.offered}`);
    if(c.cancelled)parts.push(`取消 ${c.cancelled}`);
    const summaryText=group.hasExecution?parts.slice(0,2).join(' · '):`尚未承接 · ${parts.join(' · ')}`;
    return {kind: 'group', id: group.id, ref: {authority_id: 'ui-view', task_id: group.id}, title: group.title, goal: group.title, expectedResult: group.summaryText, relations: [], state: 'group', summaryText, memberIds: [...group.memberIds], groupSize: group.counts.total};
  });
}

export function focusNeighborhood(tasks, focusId, limit = 24) {
  const bound = Number.isFinite(limit) ? Math.max(1, Math.floor(limit)) : 24;
  if (tasks.length <= bound) return {tasks: [...tasks], hiddenCount: 0};
  const {byRef, neighbors} = connections(tasks);
  const focus = typeof focusId === 'object' && focusId ? byRef.get(key(focusId)) : tasks.find(task => task.id === focusId);
  const first = focus || tasks[0];
  const pending = [key(first.ref)], included = new Set(pending);
  for (let index = 0; index < pending.length && included.size < bound; index++) {
    for (const neighbor of neighbors.get(pending[index])) {
      if (included.has(neighbor)) continue;
      included.add(neighbor); pending.push(neighbor);
      if (included.size === bound) break;
    }
  }
  const visible = tasks.filter(task => included.has(key(task.ref)));
  return {tasks: visible, hiddenCount: tasks.length - visible.length};
}
