const SVG = 'http://www.w3.org/2000/svg';
const states = {offered: '待领取', claimed: '已领取', active: '执行中', submitted: '待验收', completed: '已完成', rejected: '已拒绝', failed: '执行失败', cancelled: '已取消'};
const relationLabels = {parent: '子任务', related: '关联', depends_on: '依赖', external: '外部关联'};
const refKey = ref => JSON.stringify([ref?.authority_id || '', ref?.task_id || '']);

export function historyRound(generation) {
  if (generation === 0) return '领取前';
  return Number.isInteger(generation) && generation > 0 ? `第 ${generation} 轮承接` : '';
}

// Keep the authority's order; relationships and timestamps never reorder history.
export function visibleHistory(events, ids) {
  const seen = new Set();
  return events.filter(event => {
    if (!event.event_id || !ids.has(event.task_id) || seen.has(event.event_id)) return false;
    seen.add(event.event_id);
    return true;
  });
}

// Only explicit parent relations determine hierarchy. Other relations stay visible
// without inventing ownership, ordering or state propagation.
export function layoutTasks(tasks) {
  const nodes = new Map(tasks.map(task => [task.id, {task, parent: null, children: [], x: 0, y: 0}]));
  const byRef = new Map(tasks.map(task => [refKey(task.ref), task.id]));
  const edges = [], external = [];
  for (const task of tasks) {
    for (const relation of task.relations || []) {
      const target = relation.task || relation.target;
      const kind = relation.kind;
      const parentId = byRef.get(refKey(target));
      if (!parentId) { external.push({taskId: task.id, kind, target}); continue; }
      if (parentId === task.id) continue;
      edges.push({from: parentId, to: task.id, kind, branchKind: task.branchKind || null});
      const node = nodes.get(task.id);
      if (kind !== 'parent' || node.parent) continue;
      let cursor = nodes.get(parentId), cycle = false;
      while (cursor) {
        if (cursor.task.id === task.id) { cycle = true; break; }
        cursor = cursor.parent ? nodes.get(cursor.parent) : null;
      }
      if (!cycle) node.parent = parentId;
    }
  }
  for (const node of nodes.values()) if (node.parent) nodes.get(node.parent).children.push(node);
  const roots = [...nodes.values()].filter(node => !node.parent);
  let row = 0;
  function place(root) {
    const pending = [{node: root, depth: 0, finish: false}];
    while (pending.length) {
      const {node, depth, finish} = pending.pop();
      node.x = 130 + depth * 265;
      if (!node.children.length) { node.y = 100 + row++ * 155; continue; }
      if (!finish) {
        pending.push({node, depth, finish: true});
        for (let index = node.children.length - 1; index >= 0; index--) pending.push({node: node.children[index], depth: depth + 1, finish: false});
        continue;
      }
      for (const [index, child] of node.children.entries()) {
        // A small leaf-only fan opens the middle branch without shifting any
        // descendants or changing the relationship's semantic depth.
        if (!child.children.length && node.children.length > 2) child.x += 90 * (1 - Math.abs(index / (node.children.length - 1) * 2 - 1));
      }
      node.y = (node.children[0].y + node.children.at(-1).y) / 2;
    }
  }
  const components = [];
  for (const node of roots) {
    row = 0;
    place(node);
    const members = [], pending = [node];
    while (pending.length) { const member = pending.pop(); members.push(member); pending.push(...member.children); }
    components.push({members, width: Math.max(...members.map(n => n.x)) + 140, height: Math.max(...members.map(n => n.y)) + 120});
  }
  const shelfWidth = Math.max(700, Math.sqrt(components.reduce((area, c) => area + c.width * c.height, 0)) * 1.5);
  let shelfX = 0, shelfY = 0, shelfHeight = 0;
  for (const component of components) {
    if (shelfX && shelfX + component.width > shelfWidth) { shelfY += shelfHeight + 35; shelfX = 0; shelfHeight = 0; }
    for (const node of component.members) { node.x += shelfX; node.y += shelfY; }
    shelfX += component.width + 25;
    shelfHeight = Math.max(shelfHeight, component.height);
  }
  return {nodes, edges, external, width: Math.max(300, ...[...nodes.values()].map(n => n.x + 140)), height: Math.max(250, ...[...nodes.values()].map(n => n.y + 120))};
}

export function fitTransform(layout, width, height) {
  const scale = Math.min(1.25, Math.max(1, width - 40) / layout.width, Math.max(1, height - 65) / layout.height);
  return {scale, offset: {x: (width - layout.width * scale) / 2, y: (height - layout.height * scale) / 2}};
}

export function layoutGroups(tasks, width, height) {
  const columns = width >= 1000 ? 4 : width >= 650 ? 3 : 2;
  const rows = Math.ceil(tasks.length / columns);
  const contentWidth = Math.max(300, width - 24);
  const contentHeight = Math.max(rows * 165, height - 85);
  const nodes = new Map(tasks.map((task, index) => [task.id, {task, parent: null, children: [], x: contentWidth * ((index % columns) + .5) / columns, y: contentHeight * (Math.floor(index / columns) + .5) / Math.max(1, rows)}]));
  return {nodes, edges: [], external: [], width: contentWidth, height: contentHeight, columns};
}

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}
function svg(tag, attrs) {
  const node = document.createElementNS(SVG, tag);
  for (const [key, value] of Object.entries(attrs)) node.setAttribute(key, value);
  return node;
}

export class TaskGraph {
  constructor(container, {onSelect = () => {}, onInspect = () => {}} = {}) {
    this.container = container;
    this.onSelect = onSelect;
    this.onInspect = onInspect;
    this.tasks = [];
    this.buttons = new Map();
    this.seen = new Set();
    this.packets = [];
    this.timers = new Set();
    this.offset = {x: 0, y: 0};
    this.scale = 1;
    this.fitted = false;
    this.destroyed = false;
    this.historySource = [];
    this.history = [];
    this.historyIndex = 0;
    this.historyElapsed = 0;
    this.historyPlaying = false;
    this.historyFollow = true;
    this.historySpeed = 1;
    this.playbackSuspended = false;
    this.historyInitialized = false;
    this.intersecting = true;
    this.motion = matchMedia('(prefers-reduced-motion: reduce)');
    this.abort = new AbortController();
    this.root = el('div', 'task-graph');
    this.root.setAttribute('aria-label', '任务关联画布');
    this.root.innerHTML = '<div class="tg-nebula" aria-hidden="true"><i></i><i></i><i></i></div><canvas class="tg-background" aria-hidden="true"></canvas><div class="tg-viewport"><div class="tg-world"><svg class="tg-wires" aria-hidden="true"></svg><div class="tg-nodes"></div></div></div><div class="tg-empty" hidden><span>每项工作，都有自己的轨迹。</span><small>发布第一个任务，开始人与 Agent 的协作。</small></div><div class="tg-help">拖动平移 · 滚轮缩放 · 点击任务查看详情</div><div class="tg-controls"><button type="button" data-action="out" aria-label="缩小画布">−</button><button type="button" data-action="fit" aria-label="适应画布">适应</button><button type="button" data-action="in" aria-label="放大画布">＋</button><span class="tg-scale">100%</span></div><div class="tg-tooltip" role="tooltip" hidden></div><div class="tg-live" aria-live="polite"></div>';
    container.append(this.root);
    this.historyBar = el('div', 'tg-history');
    this.historyBar.setAttribute('aria-label', '历史流转播放');
    this.historyBar.innerHTML = '<div class="tg-history-head"><strong>历史流转 · 全部轨迹</strong><button type="button" data-action="history-play">播放</button><button type="button" data-action="history-replay">↺ 重播</button><button type="button" data-action="history-all" hidden>返回全部轨迹</button><button type="button" class="tg-history-current" data-action="history-focus"></button><small class="tg-history-status"></small></div><div class="tg-history-steps" aria-label="按事件顺序排列的历史步骤"></div>';
    this.historyBar.hidden = true;
    this.root.append(this.historyBar);
    const controls = this.historyBar.querySelector('.tg-history-head');
    for (const [action, label] of [['history-prev', '‹'], ['history-next', '›'], ['history-follow', '跟随视窗']]) {
      const button = el('button', '', label);
      button.type = 'button'; button.dataset.action = action;
      button.setAttribute('aria-label', action === 'history-prev' ? '上一步' : action === 'history-next' ? '下一步' : '切换跟随视窗');
      controls.insertBefore(button, controls.querySelector('.tg-history-current'));
    }
    const speed = el('select', 'tg-history-speed');
    speed.setAttribute('aria-label', '历史播放速度');
    for (const value of [.5, 1, 2]) { const option = el('option', '', `${value}×`); option.value = value; option.selected = value === 1; speed.append(option); }
    controls.insertBefore(speed, controls.querySelector('.tg-history-current'));
    const details = el('details', 'tg-history-details');
    const summary = el('summary', '', '本步详情');
    summary.setAttribute('aria-label', '查看本步完整参与者、承接轮次和操作');
    details.append(summary, el('div', 'tg-history-detail-content'));
    controls.insertBefore(details, controls.querySelector('.tg-history-current'));
    this.viewport = this.root.querySelector('.tg-viewport');
    this.world = this.root.querySelector('.tg-world');
    this.nodeLayer = this.root.querySelector('.tg-nodes');
    this.wireLayer = this.root.querySelector('.tg-wires');
    this.tooltip = this.root.querySelector('.tg-tooltip');
    this.tooltip.id = `task-graph-tip-${Math.random().toString(36).slice(2)}`;
    this.background = this.root.querySelector('canvas');
    this.ctx = this.background.getContext('2d');
    this.stars = Array.from({length: 54}, (_, i) => ({x: ((i * 73 + 13) % 101) / 101, y: ((i * i * 17 + 29) % 103) / 103, phase: i * 1.73}));
    this.bindNavigation();
    const visibility = () => {
      this.historyLastTime = null;
      this.root.classList.toggle('is-suspended', document.hidden || !this.intersecting);
    };
    this.listen(document, 'visibilitychange', visibility);
    this.listen(this.motion, 'change', () => {
      if (this.motion.matches) this.historyPlaying = false;
      this.renderHistory();
    });
    this.intersection = new IntersectionObserver(entries => {
      this.intersecting = entries[0]?.isIntersecting ?? true;
      visibility();
    });
    this.intersection.observe(this.root);
    visibility();
    this.viewportSize = {width: this.viewport.clientWidth, height: this.viewport.clientHeight};
    this.resize = new ResizeObserver(() => {
      this.sizeBackground();
      this.resizeViewport();
    });
    this.resize.observe(this.root);
    this.lastFrame = 0;
    this.frame = requestAnimationFrame(time => this.animate(time));
  }

  listen(target, event, fn, options = {}) {
    target.addEventListener(event, fn, {...options, signal: this.abort.signal});
  }

  bindNavigation() {
    this.listen(this.historyBar.querySelector('.tg-history-speed'), 'change', event => { this.historySpeed = Number(event.target.value); });
    this.listen(this.root, 'click', event => {
      const action = event.target.closest('[data-action]')?.dataset.action;
      if (action === 'fit') { this.pauseFollow(); this.fit(); }
      if (action === 'in' || action === 'out') this.zoom(action === 'in' ? 1.2 : 1 / 1.2);
      if (action === 'history-play') {
        this.historyPlaying = !this.historyPlaying;
        this.historyLastTime = null;
        if (!this.historyPlaying) this.camera = null;
        else this.followHistory();
        this.renderHistory();
      }
      if (action === 'history-follow') {
        this.historyFollow = !this.historyFollow;
        if (!this.historyFollow) this.camera = null;
        else this.followHistory(true);
        this.renderHistory();
      }
      if (action === 'history-prev' || action === 'history-next') {
        this.historyPlaying = false;
        this.selectHistory(this.historyIndex + (action === 'history-prev' ? -1 : 1), {manual: true});
      }
      if (action === 'history-replay') {
        this.historyPlaying = !this.motion.matches;
        this.selectHistory(0, {manual: true});
      }
      if (action === 'history-focus') this.followHistory(true);
      if (action === 'history-all') {
        this.historyTaskId = null;
        this.refreshHistory();
      }
      const step = event.target.closest('[data-history-index]');
      if (step) {
        this.historyPlaying = false;
        this.selectHistory(Number(step.dataset.historyIndex), {manual: true});
      }
    });
    this.listen(this.viewport, 'wheel', event => {
      event.preventDefault();
      const bounds = this.viewport.getBoundingClientRect();
      this.zoom(Math.exp(-event.deltaY * .001), event.clientX - bounds.left, event.clientY - bounds.top);
    }, {passive: false});
    this.listen(this.viewport, 'pointerdown', event => {
      if (event.button !== 0 || event.target.closest('button')) return;
      this.pauseFollow();
      this.drag = {x: event.clientX, y: event.clientY, ox: this.offset.x, oy: this.offset.y, id: event.pointerId};
      this.viewport.setPointerCapture(event.pointerId);
      this.root.classList.add('is-dragging');
      this.hideTooltip();
    });
    this.listen(this.viewport, 'pointermove', event => {
      if (!this.drag || this.drag.id !== event.pointerId) return;
      this.offset.x = this.drag.ox + event.clientX - this.drag.x;
      this.offset.y = this.drag.oy + event.clientY - this.drag.y;
      this.transform();
    });
    const stop = () => { this.drag = null; this.root.classList.remove('is-dragging'); };
    this.listen(this.viewport, 'pointerup', stop);
    this.listen(this.viewport, 'pointercancel', stop);
    this.listen(this.viewport, 'lostpointercapture', stop);
    this.listen(this.root, 'keydown', event => {
      if (event.key === 'Escape') this.hideTooltip();
      if (event.target.closest('.tg-node')) return;
      if (event.key === '+') this.zoom(1.2);
      if (event.key === '-') this.zoom(1 / 1.2);
    });
  }

  setData(tasks, {selectedId = null, events = [], participants = [], scopeLabel = '当前可见任务'} = {}) {
    if (this.destroyed) return;
    this.tasks = tasks;
    this.participants = participants;
    this.scopeLabel = scopeLabel;
    this.selectedId = selectedId;
    this.groupView = tasks.length > 0 && tasks.every(task => task.kind === 'group');
    this.root.classList.toggle('is-group-view', this.groupView);
    const nextLayout = this.groupView ? layoutGroups(tasks, this.viewport.clientWidth, this.viewport.clientHeight) : layoutTasks(tasks);
    const structure = JSON.stringify([tasks.map(t => t.id), nextLayout.edges]);
    const changed = structure !== this.structure;
    if (changed) this.camera = null;
    this.structure = structure;
    this.layout = nextLayout;
    const ids = new Set(tasks.map(task => task.id));
    for (const [id, button] of this.buttons) if (!ids.has(id)) { button.remove(); this.buttons.delete(id); }
    for (const node of this.layout.nodes.values()) {
      const task = node.task;
      let button = this.buttons.get(task.id);
      if (!button) {
        button = this.makeNode(task);
        this.buttons.set(task.id, button);
        this.nodeLayer.append(button);
      }
      button.dataset.state = task.state;
      button.dataset.kind = task.kind || 'task';
      button.dataset.branch = task.branchKind || '';
      button.classList.toggle('is-root', !node.parent);
      button.classList.toggle('is-selected', task.id === selectedId);
      button.style.left = node.x + 'px';
      button.style.top = node.y + 'px';
      button.querySelector('.tg-title').textContent = task.title || task.goal || '未命名任务';
      button.querySelector('.tg-title').title = task.title || task.goal || '未命名任务';
      button.querySelector('.tg-id').textContent = task.id.length > 17 ? task.id.slice(0, 8) + '…' + task.id.slice(-5) : task.id;
      const label = states[task.state] || task.state;
      button.querySelector('.tg-state').textContent = `当前：${label}`;
      button.querySelector('.tg-symbol').textContent = task.state === 'completed' ? '✓' : task.state === 'cancelled' ? '−' : task.state === 'failed' || task.state === 'rejected' ? '!' : task.branchKind === 'variant' ? '✧' : task.branchKind === 'retry' ? '↻' : node.parent ? '◇' : '◎';
      const actor = task.assignee;
      button.querySelector('.tg-person').textContent = actor?.kind === 'human' ? 'H' : actor ? this.actorName(actor).slice(0, 1).toUpperCase() : '·';
      button.setAttribute('aria-label', `${task.title || task.goal}，${label}。${actor ? '承接者 ' + this.actorName(actor) : '尚无当前承接者'}。查看任务详情`);
      button.setAttribute('aria-pressed', String(task.id === selectedId));
      const badge = button.querySelector('.tg-branch-badge');
      badge.textContent = task.kind === 'group' ? '任务链' : {variant: '并行尝试', retry: '重做', child: '子任务'}[task.branchKind] || (node.parent ? '子任务' : '');
      if (task.kind === 'group') {
        button.querySelector('.tg-id').textContent = `${task.groupSize ?? task.memberIds?.length ?? 0} 项任务`;
        button.querySelector('.tg-symbol').textContent = String(task.groupSize ?? task.memberIds?.length ?? 0);
        button.querySelector('.tg-state').textContent = task.summaryText || '查看任务链';
        button.querySelector('.tg-person').textContent = '';
        button.setAttribute('aria-label', `${task.title || task.goal}，任务链总览，${task.summaryText || ''}。进入任务链`);
      }
    }
    this.root.querySelector('.tg-empty').hidden = tasks.length !== 0;
    this.root.querySelector('.tg-help').hidden = tasks.length === 0;
    if (changed) {
      this.renderEdges();
      if (!this.fitted) this.fit();
      else this.transform();
    }
    for (const edge of this.edges || []) {
      edge.group.dataset.state = this.layout.nodes.get(edge.to)?.task.state || '';
      edge.group.classList.toggle('is-selected', edge.from === selectedId || edge.to === selectedId);
    }
    if (this.hoverId && ids.has(this.hoverId)) this.showTooltip(this.hoverId);
    else this.hideTooltip();
    for (const event of events) this.pulse(event);
    this.refreshHistory();
  }

  setHistory(events, {loading = false, error = '', complete = true, truncated = false} = {}) {
    if (this.destroyed) return;
    if (!events.length && !loading && !error) {
      this.historyTaskId = null;
      this.historyFollow = true;
      this.camera = null;
      this.historyInitialized = false;
      this.historyPlaying = false;
      this.historyLastTime = null;
      this.seen.clear();
      for (const packet of this.packets) packet.edge.packet.setAttribute('opacity', '0');
      this.packets = [];
      for (const timer of this.timers) clearTimeout(timer);
      this.timers.clear();
      for (const button of this.buttons.values()) button.classList.remove('tg-event');
    }
    this.historySource = events;
    this.historyLoading = loading;
    this.historyError = error;
    this.historyComplete = complete;
    this.historyTruncated = truncated;
    this.refreshHistory();
  }

  refreshHistory() {
    const current = this.history[this.historyIndex]?.event_id;
    const ids = new Set(this.tasks.filter(task => task.kind !== 'group').map(task => task.id));
    if (this.historyTaskId && !ids.has(this.historyTaskId)) this.historyTaskId = null;
    this.history = visibleHistory(this.historySource, this.historyTaskId ? new Set([this.historyTaskId]) : ids);
    const index = this.history.findIndex(event => event.event_id === current);
    this.historyIndex = index < 0 ? 0 : index;
    if (index < 0) this.historyElapsed = 0;
    if (this.history.length && !this.historyInitialized) {
      this.historyInitialized = true;
      this.historyPlaying = !this.motion.matches;
      this.selectHistory(0);
    } else this.renderHistory();
  }

  replayTask(taskId) {
    if (this.destroyed || !this.tasks.some(task => task.id === taskId && task.kind !== 'group') || !visibleHistory(this.historySource, new Set([taskId])).length) return false;
    this.historyTaskId = taskId;
    this.refreshHistory();
    this.historyPlaying = !this.motion.matches;
    this.selectHistory(0, {manual: true});
    return true;
  }

  selectHistory(index, {manual = false} = {}) {
    if (!this.history.length) return;
    this.historyIndex = (index % this.history.length + this.history.length) % this.history.length;
    this.historyElapsed = 0;
    this.historyLastTime = null;
    const event = this.history[this.historyIndex];
    this.pulse(event, {historical: true});
    this.renderHistory();
    this.followHistory(manual);
  }

  renderHistory() {
    this.historyBar.hidden = (this.tasks.length > 0 && this.tasks.every(task => task.kind === 'group')) || (!this.history.length && !this.historyLoading && !this.historyError);
    this.root.classList.toggle('has-history', !this.historyBar.hidden);
    const scope = this.historyTaskId ? '单任务过程' : this.scopeLabel || '当前可见任务';
    this.historyBar.querySelector('strong').textContent = `历史流转 · ${scope}`;
    this.historyBar.querySelector('[data-action="history-all"]').hidden = !this.historyTaskId;
    this.historyBar.querySelector('[data-action="history-play"]').textContent = this.historyPlaying ? 'Ⅱ 暂停' : '▷ 播放';
    this.historyBar.querySelector('[data-action="history-follow"]').textContent = this.historyFollow ? '跟随中' : '恢复跟随';
    this.historyBar.querySelector('[data-action="history-follow"]').setAttribute('aria-pressed', String(this.historyFollow));
    for (const button of this.historyBar.querySelectorAll('button')) button.disabled = !this.history.length;
    const event = this.history[this.historyIndex];
    const title = event ? this.layout?.nodes.get(event.task_id)?.task.title || event.task_id : '';
    const detail = event ? [`任务：${title}`, `参与者：${event.actor ? this.actorName(event.actor) : '未记录'}`, `阶段：${historyRound(event.generation) || '承接轮次未记录'}`, `操作：${event.label || event.operation}`].join('\n') : '暂无可见任务事件';
    const current = this.historyBar.querySelector('.tg-history-current');
    current.textContent = event ? `${this.historyIndex + 1} / ${this.history.length} · ${title} · ${event.actor ? this.actorName(event.actor) + ' · ' : ''}${historyRound(event.generation)} · ${event.label || event.operation}` : '暂无可见任务事件';
    current.title = detail;
    current.setAttribute('aria-label', `${detail}。定位此任务`);
    this.historyBar.querySelector('.tg-history-detail-content').textContent = detail;
    this.historyBar.querySelector('.tg-history-details').hidden = !event;
    this.historyBar.querySelector('.tg-history-status').textContent = this.historyError ? '加载失败 · 已有历史保留' : this.historyLoading ? '加载中…' : this.historyTruncated ? `近期事件 · ${scope}` : this.historyComplete === false ? `部分历史 · ${scope}` : `事件顺序 · ${scope}`;
    this.historyBar.querySelector('.tg-history-status').title = '编号表示事件顺序。图中流光仅装饰关联路径，不代表依赖顺序。节点始终显示当前状态。';
    const steps = this.historyBar.querySelector('.tg-history-steps');
    const signature = JSON.stringify(this.history.map(item => [item.event_id, item.label, item.operation, item.generation]));
    if (signature !== this.historySignature) {
      this.historySignature = signature;
      steps.replaceChildren(...this.history.map((item, index) => {
        const round = historyRound(item.generation);
        const button = el('button', 'tg-history-step', `${index + 1} · ${item.label || item.operation}${round ? ' · ' + round : ''}`);
        button.type = 'button';
        button.dataset.historyIndex = index;
        button.title = `${item.task_id} · ${item.label || item.operation}`;
        button.setAttribute('aria-label', `历史步骤 ${index + 1}，${button.title}，定位任务`);
        return button;
      }));
    }
    for (const [index, button] of [...steps.children].entries()) {
      button.classList.toggle('is-current', index === this.historyIndex);
      button.setAttribute('aria-current', index === this.historyIndex ? 'step' : 'false');
    }
    const activeStep = steps.children[this.historyIndex];
    if (activeStep && (activeStep.offsetLeft < steps.scrollLeft || activeStep.offsetLeft + activeStep.offsetWidth > steps.scrollLeft + steps.clientWidth)) steps.scrollLeft = activeStep.offsetLeft - steps.clientWidth / 2;
    const firstSteps = new Map();
    this.history.forEach((item, index) => { if (!firstSteps.has(item.task_id)) firstSteps.set(item.task_id, index + 1); });
    for (const [id, button] of this.buttons) {
      button.classList.toggle('has-history', firstSteps.has(id));
      button.classList.toggle('is-history-current', event?.task_id === id);
      button.querySelector('.tg-history-mark').textContent = firstSteps.has(id) ? `历史 #${event?.task_id === id ? this.historyIndex + 1 : firstSteps.get(id)}` : '';
      const phase = button.querySelector('.tg-history-phase');
      const round = historyRound(event?.generation);
      phase.textContent = event?.task_id === id ? `回放 · ${event.actor ? this.actorName(event.actor) + ' · ' : ''}${event.label || event.operation}${round ? ' · ' + round : ''}` : '';
      phase.title = phase.textContent;
    }
    for (const edge of this.edges || []) edge.group.classList.toggle('has-history', firstSteps.has(edge.from) || firstSteps.has(edge.to));
  }

  advanceHistory(time) {
    const previous = this.historyLastTime;
    this.historyLastTime = time;
    if (!this.historyPlaying || !this.history.length || previous == null || this.playbackBlocked?.()) return;
    this.historyElapsed += Math.min(100, Math.max(0, time - previous)) * (this.historySpeed || 1);
    if (this.historyElapsed >= 2400) this.selectHistory((this.historyIndex + 1) % this.history.length);
  }

  makeNode(task) {
    const button = el('button', 'tg-node');
    button.type = 'button';
    button.dataset.taskId = task.id;
    button.innerHTML = '<span class="tg-id"></span><span class="tg-orb"><span class="tg-ring"></span><span class="tg-wave"></span><span class="tg-symbol"></span><span class="tg-person"></span></span><span class="tg-title"></span><span class="tg-state"></span>';
    button.append(el('span', 'tg-history-mark'));
    button.append(el('span', 'tg-history-phase'));
    button.append(el('span', 'tg-branch-badge'));
    this.listen(button, 'click', () => { this.hideTooltip(); this.onSelect(task.id); });
    this.listen(button, 'pointerenter', () => this.showTooltip(task.id));
    this.listen(button, 'pointerleave', () => this.hideTooltip());
    this.listen(button, 'focus', () => { this.ensureVisible(task.id); this.showTooltip(task.id); });
    this.listen(button, 'blur', () => this.hideTooltip());
    return button;
  }

  renderEdges() {
    this.wireLayer.replaceChildren();
    this.packets = [];
    this.edges = [];
    this.wireLayer.setAttribute('width', this.layout.width);
    this.wireLayer.setAttribute('height', this.layout.height);
    const seen = new Set();
    for (const edge of this.layout.edges) {
      const key = JSON.stringify([edge.from, edge.to, edge.kind]);
      if (seen.has(key)) continue;
      seen.add(key);
      const a = this.layout.nodes.get(edge.from), b = this.layout.nodes.get(edge.to);
      const ay = a.y - 24, by = b.y - 24;
      let d;
      if (a.x === b.x) d = `M${a.x},${ay} C${a.x + 140},${ay} ${b.x + 140},${by} ${b.x},${by}`;
      else { const bend = Math.max(80, Math.abs(a.x - b.x) * .45); d = `M${a.x},${ay} C${a.x + bend},${ay} ${b.x - bend},${by} ${b.x},${by}`; }
      const group = svg('g', {'data-kind': edge.kind, 'data-branch': edge.branchKind || ''});
      const path = svg('path', {d, class: 'tg-wire'});
      const sheen = svg('path', {d, class: 'tg-wire-sheen'});
      const caption = svg('text', {x: (a.x + b.x) / 2 + (a.x === b.x ? 90 : 0), y: (ay + by) / 2 - 12, 'text-anchor': 'middle', class: 'tg-edge-label'});
      caption.textContent = edge.branchKind === 'variant' ? '并行尝试' : edge.branchKind === 'retry' ? '再次探索' : relationLabels[edge.kind] || edge.kind;
      const packet = svg('circle', {r: '3', class: 'tg-packet', opacity: '0'});
      group.append(path, sheen, caption, packet);
      this.wireLayer.append(group);
      this.edges.push({...edge, group, path, packet, length: path.getTotalLength()});
    }
  }

  actorName(actor) {
    if (!actor) return '未承接';
    return this.participants?.find(p => p.actor?.kind === actor.kind && p.actor?.id === actor.id)?.display_name || actor.id;
  }

  showTooltip(id) {
    const task = this.layout?.nodes.get(id)?.task;
    const button = this.buttons.get(id);
    if (!task || !button || this.drag) return;
    this.hoverId = id;
    const isGroup = task.kind === 'group';
    const top = el('div', 'tg-tip-top');
    top.append(el('span', '', isGroup ? '任务链总览' : task.id), el('span', '', isGroup ? `${task.groupSize ?? task.memberIds?.length ?? 0} 项任务` : states[task.state] || task.state));
    const detail = el('p', 'tg-tip-summary', (task.expectedResult || task.goal || '').slice(0, 180));
    const bottom = el('div', 'tg-tip-bottom');
    bottom.append(el('span', '', isGroup ? task.summaryText || '聚合视图' : task.assignee ? '承接者 · ' + this.actorName(task.assignee) : '发起者 · ' + this.actorName(task.issuer)), el('span', '', isGroup ? '进入任务链 ↗' : '点击查看 ↗'));
    this.tooltip.replaceChildren(top, el('h3', '', task.title || task.goal || '任务'), detail, bottom);
    const external = this.layout.external.filter(e => e.taskId === id);
    if (external.length) this.tooltip.append(el('small', 'tg-external-note', `${external.length} 个画布外关联 · 可在详情查看`));
    const history = isGroup ? [] : visibleHistory(this.historySource, new Set([id]));
    if (history.length) {
      const trail = el('div', 'tg-tip-history');
      trail.append(el('small', '', history.length > 8 ? `历史轨迹 · 近 8 步 / 共 ${history.length} 步` : `历史轨迹 · ${history.length} 步`));
      trail.append(el('p', '', history.slice(-8).map((event, index) => `${Math.max(0, history.length - 8) + index + 1}. ${event.label || event.operation}`).join(' → ')));
      this.tooltip.append(trail);
    }
    this.tooltip.hidden = false;
    button.setAttribute('aria-describedby', this.tooltip.id);
    const rb = this.root.getBoundingClientRect(), bb = button.getBoundingClientRect();
    const width = this.tooltip.offsetWidth, height = this.tooltip.offsetHeight;
    let x = bb.right - rb.left + 10;
    if (x + width > rb.width - 12) x = bb.left - rb.left - width - 10;
    this.tooltip.style.left = Math.max(12, Math.min(x, rb.width - width - 12)) + 'px';
    this.tooltip.style.top = Math.max(12, Math.min(bb.top - rb.top, rb.height - height - 12)) + 'px';
    for (const edge of this.edges || []) edge.group.classList.toggle('is-hovered', edge.from === id || edge.to === id);
    this.onInspect(id);
  }

  hideTooltip() {
    this.tooltip.hidden = true;
    if (this.hoverId) this.buttons.get(this.hoverId)?.removeAttribute('aria-describedby');
    this.hoverId = null;
    for (const edge of this.edges || []) edge.group.classList.remove('is-hovered');
  }

  pulse(event, {historical = false} = {}) {
    if (!event.event_id || (!historical && this.seen.has(event.event_id))) return;
    if (!historical) this.seen.add(event.event_id);
    if (this.seen.size > 3000) this.seen.delete(this.seen.values().next().value);
    const id = event.task_id || event.task?.task_id;
    const button = this.buttons.get(id);
    if (!button) return;
    const effect = event.operation === 'task.reopen' ? 'retry' : event.operation === 'task.create' ? 'spawn' : event.operation === 'run.submit' ? 'submit' : event.operation === 'task.decide' ? 'decision' : 'report';
    button.classList.remove('tg-event');
    void button.offsetWidth;
    button.dataset.effect = effect;
    button.classList.add('tg-event');
    const timer = setTimeout(() => { button.classList.remove('tg-event'); this.timers.delete(timer); }, 1600);
    this.timers.add(timer);
    if (!historical && !this.motion.matches) for (const edge of this.edges || []) if (edge.to === id) this.packets.push({edge, started: performance.now(), reverse: event.operation === 'run.report' || event.operation === 'run.submit', color: effect === 'submit' ? '#edc28a' : effect === 'retry' ? '#bba1ef' : '#8cead9'});
    if (!historical) this.root.querySelector('.tg-live').textContent = `${this.layout.nodes.get(id)?.task.title || id} · ${event.operation}`;
  }

  transform() {
    this.world.style.transform = `translate(${this.offset.x}px,${this.offset.y}px) scale(${this.scale})`;
    const percent = this.scale * 100;
    this.root.querySelector('.tg-scale').textContent = (percent < 10 ? percent.toFixed(1) : Math.round(percent)) + '%';
    this.root.classList.toggle('is-zoomed-out', this.scale < .65);
    this.root.querySelector('.tg-help').textContent = this.scale < .35 ? '全图总览 · 搜索并聚焦任务可放大阅读' : '拖动平移 · 滚轮缩放 · 点击任务查看详情';
    this.hideTooltip();
  }

  setPlaybackSuspended(suspended) {
    this.playbackSuspended = Boolean(suspended);
    this.historyLastTime = null;
    this.camera = null;
  }

  playbackBlocked() {
    return this.playbackSuspended || document.hidden || !this.intersecting || Boolean(this.historyBar?.querySelector('.tg-history-details')?.open) || Boolean(document.activeElement?.closest('input, textarea, [contenteditable="true"]'));
  }

  pauseFollow() {
    this.historyFollow = false;
    this.camera = null;
    this.renderHistory();
  }

  readingCenter() {
    const width = this.viewport.clientWidth, height = this.viewport.clientHeight;
    const bar = this.historyBar;
    const bottom = bar && !bar.hidden ? Math.min(height - 65, bar.offsetTop - 16) : height - 65;
    const button = this.buttons.get(this.history[this.historyIndex]?.task_id);
    const scale = Math.min(1.1, Math.max(.9, this.scale));
    const half = (button?.offsetHeight || 130) * scale / 2;
    const phase = button?.querySelector('.tg-history-phase');
    const below = half + ((phase?.offsetHeight || 0) + 9) * scale;
    // Account for the title/state/phase below the orb, rather than centering
    // an asymmetric button in a fictitious minimum 200px area.
    const above = half + (height < 450 && width <= 650 ? 24 : 38) * scale;
    return {x: width / 2, y: (8 + above + bottom - below) / 2};
  }

  followHistory(manual = false) {
    if (this.playbackBlocked() || (!manual && (!this.historyFollow || !this.historyPlaying))) return;
    const node = this.layout?.nodes.get(this.history[this.historyIndex]?.task_id);
    if (!node) return;
    const center = this.readingCenter();
    const scale = Math.min(1.1, Math.max(.9, this.scale));
    const target = {x: center.x - node.x * scale, y: center.y - node.y * scale};
    if (this.motion.matches) { this.scale = scale; this.offset = target; this.camera = null; this.transform(); return; }
    this.camera = {started: performance.now(), from: {...this.offset}, fromScale: this.scale, target, scale};
  }

  advanceCamera(time) {
    if (!this.camera) return;
    if (this.playbackBlocked()) { this.camera = null; return; }
    const camera = this.camera;
    const progress = Math.min(1, Math.max(0, (time - camera.started) / 650));
    const eased = 1 - (1 - progress) ** 3;
    this.scale = camera.fromScale + (camera.scale - camera.fromScale) * eased;
    this.offset = {x: camera.from.x + (camera.target.x - camera.from.x) * eased, y: camera.from.y + (camera.target.y - camera.from.y) * eased};
    this.transform();
    if (progress === 1) this.camera = null;
  }

  fit() {
    this.camera = null;
    if (!this.layout || !this.tasks.length || !this.viewport.clientWidth) return;
    const fitted = this.groupView ? {scale: 1, offset: {x: (this.viewport.clientWidth - this.layout.width) / 2, y: Math.max(10, (this.viewport.clientHeight - 65 - this.layout.height) / 2)}} : fitTransform(this.layout, this.viewport.clientWidth, this.viewport.clientHeight);
    this.scale = fitted.scale;
    this.offset = fitted.offset;
    this.fitted = true;
    this.transform();
  }

  resizeViewport() {
    const width = this.viewport.clientWidth, height = this.viewport.clientHeight;
    if (!width || !height) return;
    const previous = this.viewportSize;
    this.viewportSize = {width, height};
    if (this.groupView) {
      this.layout = layoutGroups(this.tasks, width, height);
      for (const [id, node] of this.layout.nodes) {
        const button = this.buttons.get(id);
        button.style.left = node.x + 'px';
        button.style.top = node.y + 'px';
      }
      this.fit();
      return;
    }
    if (!this.fitted) {
      if (this.tasks.length) this.fit();
      return;
    }
    if (!previous?.width || !previous?.height || (previous.width === width && previous.height === height)) return;
    // Preserve the world point at the viewport center, including user panning.
    // Resizing must not change zoom, selection or keyboard focus.
    this.offset.x += (width - previous.width) / 2;
    this.offset.y += (height - previous.height) / 2;
    this.camera = null;
    this.transform();
    // Resize may change the playback strip's height on a narrow screen.
    if (this.historyFollow && this.history?.length && !this.playbackBlocked()) this.followHistory(true);
  }

  zoom(factor, x = this.viewport.clientWidth / 2, y = this.viewport.clientHeight / 2) {
    this.pauseFollow?.();
    const previous = this.scale;
    const minimum = this.layout ? Math.min(.25, fitTransform(this.layout, this.viewport.clientWidth, this.viewport.clientHeight).scale) : .25;
    this.scale = Math.max(minimum, Math.min(2, previous * factor));
    this.offset.x = x - (x - this.offset.x) * this.scale / previous;
    this.offset.y = y - (y - this.offset.y) * this.scale / previous;
    this.transform();
  }

  focus(id) {
    this.camera = null;
    const node = this.layout?.nodes.get(id);
    if (!node) return;
    this.scale = Math.max(.9, this.scale);
    this.offset = {x: this.viewport.clientWidth / 2 - node.x * this.scale, y: this.viewport.clientHeight / 2 - node.y * this.scale};
    this.transform();
    this.buttons.get(id)?.focus({preventScroll: true});
  }

  ensureVisible(id) {
    const node = this.layout?.nodes.get(id);
    if (!node) return;
    const x = node.x * this.scale + this.offset.x, y = node.y * this.scale + this.offset.y;
    if (x < 70 || x > this.viewport.clientWidth - 70 || y < 80 || y > this.viewport.clientHeight - 100) {
      this.offset = {x: this.viewport.clientWidth / 2 - node.x * this.scale, y: this.viewport.clientHeight / 2 - node.y * this.scale};
      this.transform();
    }
  }

  sizeBackground() {
    this.width = this.root.clientWidth;
    this.height = this.root.clientHeight;
    const dpr = Math.min(devicePixelRatio || 1, 2);
    this.background.width = Math.round(this.width * dpr);
    this.background.height = Math.round(this.height * dpr);
    this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    this.drawBackground(0);
  }

  drawBackground(time) {
    const {ctx, width: w, height: h} = this;
    if (!w || !h) return;
    ctx.clearRect(0, 0, w, h);
    const pts = this.stars.map(s => ({x: s.x * w + Math.sin(time * .00013 + s.phase) * 20, y: s.y * h + Math.cos(time * .00011 + s.phase) * 18}));
    for (let i = 0; i < pts.length; i++) {
      const a = pts[i];
      ctx.fillStyle = i % 3 === 0 ? '#a28ae16a' : '#72c0d565';
      ctx.beginPath(); ctx.arc(a.x, a.y, i % 4 === 0 ? 1.15 : .7, 0, Math.PI * 2); ctx.fill();
      for (let j = i + 1; j < pts.length; j++) {
        const b = pts[j], distance = Math.hypot(a.x - b.x, a.y - b.y), limit = w < 600 ? 95 : 145;
        if (distance >= limit) continue;
        ctx.strokeStyle = `rgba(130,146,211,${(1 - distance / limit) * .16})`;
        ctx.lineWidth = .65; ctx.beginPath(); ctx.moveTo(a.x, a.y); ctx.lineTo(b.x, b.y); ctx.stroke();
      }
    }
  }

  animate(time) {
    if (this.destroyed) return;
    if (!document.hidden && this.intersecting) this.advanceHistory(time);
    else this.historyLastTime = null;
    this.advanceCamera(time);
    if (!document.hidden && this.intersecting && !this.motion.matches) {
      if (time - this.lastFrame > 50) { this.drawBackground(time); this.lastFrame = time; }
      this.packets = this.packets.filter(packet => {
        const progress = (time - packet.started) / 1500;
        if (progress >= 1) { packet.edge.packet.setAttribute('opacity', '0'); return false; }
        const point = packet.edge.path.getPointAtLength(packet.edge.length * (packet.reverse ? 1 - progress : progress));
        packet.edge.packet.setAttribute('cx', point.x); packet.edge.packet.setAttribute('cy', point.y);
        packet.edge.packet.setAttribute('fill', packet.color); packet.edge.packet.setAttribute('opacity', String(Math.sin(Math.PI * progress)));
        return true;
      });
    }
    this.frame = requestAnimationFrame(t => this.animate(t));
  }

  destroy() {
    this.destroyed = true;
    cancelAnimationFrame(this.frame);
    this.resize.disconnect();
    this.intersection.disconnect();
    this.abort.abort();
    for (const timer of this.timers) clearTimeout(timer);
    this.root.remove();
    this.buttons.clear();
  }
}
