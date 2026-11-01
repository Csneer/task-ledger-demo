import {OPERATIONS} from './model.js';

const key = event => JSON.stringify([event.authority_id, event.event_id]);

// Log order is authoritative. Timestamps, UUIDs and relations are not sort keys.
export function historyEvent(entry) {
  const event = entry.event;
  if (entry.source !== 'core' || !event?.event_id || !event.task) return null;
  const decision = event.records?.find(record => record.record_type === 'Decision')?.record;
  const label = event.operation === 'task.decide'
    ? decision?.outcome === 'accepted' ? '验收通过' : decision?.outcome === 'rejected' ? '退回修改' : '验收决定'
    : OPERATIONS[event.operation] || event.operation;
  return {
    event_id: event.event_id, authority_id: event.task.authority_id,
    task_id: event.task.task_id, operation: event.operation, label,
    revision: event.revision, actor: event.actor, recorded_at: entry.recorded_at,
    state: event.task_snapshot?.state, generation: event.task_snapshot?.generation,
  };
}

// This reader owns a separate cursor. A newer workspace snapshot must never
// skip the interval that the history reader has not consumed yet.
export class HistoryFeed {
  constructor(query, onChange, {maxEvents = 1200, pageBudget = 5} = {}) {
    this.query = query;
    this.onChange = onChange;
    this.maxEvents = maxEvents;
    this.pageBudget = pageBudget;
    this.epoch = 0;
    this.reset();
  }

  reset() {
    this.epoch++;
    this.busy = false;
    this.cursor = null;
    this.events = [];
    this.staged = [];
    this.complete = false;
    this.truncated = false;
    this.publish(false);
  }

  publish(loading, error = '') {
    this.onChange(this.events, {loading, error, complete: this.complete, truncated: this.truncated});
  }

  async read() {
    if (this.busy) return;
    this.busy = true;
    const epoch = this.epoch;
    if (!this.complete) this.publish(true);
    try {
      if (!this.cursor) {
        const opened = await this.query('events.open', {});
        if (epoch !== this.epoch) return;
        this.cursor = opened.cursor;
      }
      for (let page = 0; page < this.pageBudget; page++) {
        const batch = await this.query('events.read', {cursor: this.cursor, limit: 200});
        if (epoch !== this.epoch) return;
        const seen = new Set([...this.events, ...this.staged].map(key));
        for (const entry of batch.events) {
          const projected = historyEvent(entry);
          if (!projected || seen.has(key(projected))) continue;
          seen.add(key(projected));
          this.staged.push(projected);
        }
        if (this.staged.length > this.maxEvents) {
          this.staged = this.staged.slice(-this.maxEvents);
          this.truncated = true;
        }
        this.cursor = batch.done ? batch.follow_cursor : batch.next_cursor;
        if (batch.done) {
          const merged = [...this.events, ...this.staged];
          this.truncated ||= merged.length > this.maxEvents;
          this.events = merged.slice(-this.maxEvents);
          this.staged = [];
          this.complete = true;
          this.publish(false);
          return;
        }
        this.complete = false;
      }
      // Keep the last complete history visible until this fixed batch is drained.
      this.publish(true);
    } catch (error) {
      if (epoch !== this.epoch) return;
      if (error.code === 'CURSOR_RESET_REQUIRED') {
        this.cursor = null;
        this.events = [];
        this.staged = [];
        this.complete = false;
        this.truncated = false;
      }
      this.publish(false, error.code === 'CURSOR_RESET_REQUIRED'
        ? '历史游标已更新，正在重新读取'
        : '历史读取暂时中断，将自动重试');
    } finally {
      if (epoch === this.epoch) this.busy = false;
    }
  }
}
