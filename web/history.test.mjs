import test from 'node:test';
import assert from 'node:assert/strict';
import {HistoryFeed, historyEvent} from './history.js';

const entry = (id, operation = 'task.create', extra = {}) => ({source: 'core', recorded_at: '2026-11-01T00:00:00Z', event: {event_id: id, task: {authority_id: 'a', task_id: 't'}, operation, revision: 1, ...extra}});

test('history preserves service order and publishes only a complete event batch', async () => {
  const calls = [], changes = [];
  const responses = [{cursor: 'open'}, {events: [entry('z')], done: false, next_cursor: 'page2'}, {events: [entry('a'), entry('z')], done: true, follow_cursor: 'follow'}, {events: [entry('next')], done: true, follow_cursor: 'follow2'}];
  const feed = new HistoryFeed(async (operation, body) => {calls.push([operation, body.cursor]); return responses.shift();}, (events, meta) => changes.push({events: [...events], ...meta}), {pageBudget: 1});
  await feed.read();assert.deepEqual(feed.events, []);assert.equal(changes.at(-1).loading, true);
  await feed.read();assert.deepEqual(feed.events.map(e => e.event_id), ['z', 'a']);
  await feed.read();assert.deepEqual(feed.events.map(e => e.event_id), ['z', 'a', 'next']);
  assert.deepEqual(calls, [['events.open', undefined], ['events.read', 'open'], ['events.read', 'page2'], ['events.read', 'follow']]);
});

test('history labels distinguish accepted and rejected decisions, not merely decide', () => {
  for (const [outcome, label] of [['accepted', '验收通过'], ['rejected', '退回修改']]) {
    assert.equal(historyEvent(entry(outcome, 'task.decide', {records: [{record_type: 'Decision', record: {outcome}}]})).label, label);
  }
  assert.equal(historyEvent({...entry('app'), source: 'application'}), null);
});

test('failed page retries the same cursor and retains already read pages', async () => {
  let page2Calls = 0;
  const feed = new HistoryFeed(async (operation, body) => {
    if (operation === 'events.open') return {cursor: 'first'};
    if (body.cursor === 'first') return {events: [entry('first')], done: false, next_cursor: 'second'};
    assert.equal(body.cursor, 'second');
    if (!page2Calls++) throw new Error('offline');
    return {events: [entry('second')], done: true, follow_cursor: 'next'};
  }, () => {});
  await feed.read();assert.equal(feed.cursor, 'second');assert.deepEqual(feed.events, []);
  await feed.read();assert.deepEqual(feed.events.map(e => e.event_id), ['first', 'second']);
});

test('identity reset discards an old in-flight response', async () => {
  let finish;
  const feed = new HistoryFeed(() => new Promise(resolve => {finish = resolve;}), () => {});
  const pending = feed.read();feed.reset();finish({cursor: 'old-identity'});await pending;
  assert.equal(feed.cursor, null);assert.deepEqual(feed.events, []);assert.equal(feed.busy, false);
});

test('retention explicitly reports a truncated recent history', async () => {
  let last;
  const feed = new HistoryFeed(async operation => operation === 'events.open' ? {cursor: 'start'} : {events: [entry('1'), entry('2'), entry('3')], done: true, follow_cursor: 'end'}, (events, meta) => {last = meta;}, {maxEvents: 2});
  await feed.read();assert.deepEqual(feed.events.map(e => e.event_id), ['2', '3']);assert.equal(last.truncated, true);assert.equal(last.complete, true);
});

test('a task-scoped reader independently recovers events evicted by the workspace window', async () => {
  const old=entry('old'),newer={...entry('new'),event:{...entry('new').event,task:{authority_id:'a',task_id:'other'}}};
  const global=new HistoryFeed(async op=>op==='events.open'?{cursor:'global'}:{events:[old,newer],done:true,follow_cursor:'g-next'},()=>{},{maxEvents:1});
  await global.read();assert.deepEqual(global.events.map(e=>e.event_id),['new']);
  const calls=[];const scoped=new HistoryFeed(async(op,body)=>{const request=op==='events.open'?{task:{authority_id:'a',task_id:'t'}}:body;calls.push([op,request]);return op==='events.open'?{cursor:'task-only'}:{events:[old],done:true,follow_cursor:'task-next'};},()=>{});
  await scoped.read();assert.equal(scoped.events[0].event_id,'old');assert.deepEqual(calls[0][1],{task:{authority_id:'a',task_id:'t'}});assert.deepEqual(calls[1][1],{cursor:'task-only',limit:200});assert.equal(global.cursor,'g-next');
});

test('a stale read after reset cannot publish or unlock a newer read', async () => {
  let finishOld,finishNew,opened=0;
  const feed=new HistoryFeed(async op=>{if(op==='events.open')return {cursor:++opened===1?'old':'new'};return new Promise(resolve=>{if(opened===1)finishOld=resolve;else finishNew=resolve;});},()=>{});
  const oldRead=feed.read();await new Promise(resolve=>setImmediate(resolve));feed.reset();const newRead=feed.read();await new Promise(resolve=>setImmediate(resolve));
  finishOld({events:[entry('old')],done:true,follow_cursor:'stale'});await oldRead;assert.equal(feed.busy,true);assert.deepEqual(feed.events,[]);
  finishNew({events:[entry('new')],done:true,follow_cursor:'current'});await newRead;assert.equal(feed.busy,false);assert.equal(feed.cursor,'current');assert.deepEqual(feed.events.map(e=>e.event_id),['new']);
});
