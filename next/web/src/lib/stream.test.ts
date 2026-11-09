import { test } from "node:test";
import assert from "node:assert/strict";
import { stream, watchHello, watchStreamState, watchTranscripts, type EventConnection } from "./stream.ts";
import { decodeEvent } from "./event.ts";

test("skips malformed event bodies at the stream boundary", () => {
  assert.equal(decodeEvent({
    seq: 1, id: "e", at: "", from: { kind: "owner" },
    type: "session.started", body: { session: "s" },
  }), undefined);
});

test("delivers the build hello", () => {
  let listener: (event: MessageEvent<string>) => void = () => {};
  let build = "";
  watchHello(hello => { build = hello.build; });
  const stop = stream(undefined, () => 0, () => {}, () => ({
    onmessage: null, onerror: null, addEventListener(type, callback) { if (type === "hello") listener = callback; }, close() {},
  }));
  listener(new MessageEvent("hello", { data: '{"build":"b"}' }));
  assert.equal(build, "b");
  stop();
});

test("notifies transcript watchers on owner reconnect and changes", () => {
  const calls: (string | undefined)[] = [];
  const unwatch = watchTranscripts(session => calls.push(session));
  let hello: (event: MessageEvent<string>) => void = () => {};
  let reconnect = () => {};
  const sources: EventConnection[] = [];
  const stop = stream(undefined, () => 0, () => {}, () => {
    const source: EventConnection = { onmessage: null, onerror: null, addEventListener(type, cb) { if (type === "hello") hello = cb; }, close() {} };
    sources.push(source);
    return source;
  }, cb => { reconnect = cb; return () => {}; });
  hello(new MessageEvent("hello", { data: '{"build":"b"}' }));
  sources[0].onmessage?.(new MessageEvent("message", { data: JSON.stringify({
    seq: 0, id: "", at: "", from: { kind: "engine" },
    type: "transcript.changed", body: { session: "s", turn: "t" },
  }) }));
  sources[0].onerror?.(new Event("error"));
  reconnect();
  hello(new MessageEvent("hello", { data: '{"build":"b"}' }));
  assert.deepEqual(calls, [undefined, "s", undefined]);
  unwatch();
  stop();
});

const ended = (outcome: string, seq: number) => JSON.stringify({
  seq, id: "", at: "", from: { kind: "session", id: "s" },
  type: "turn.ended", body: { session: "s", turn: "t", outcome, usage: { model: "m", inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 } },
});

for (const outcome of ["completed", "failed", "stopped", "closed"]) {
  test(`a room stream's ${outcome} turn.ended makes transcript watchers read again`, () => {
    const calls: (string | undefined)[] = [];
    const unwatch = watchTranscripts(session => calls.push(session));
    const sources: EventConnection[] = [];
    const stop = stream("room", () => 0, () => {}, () => {
      const source: EventConnection = { onmessage: null, onerror: null, addEventListener() {}, close() {} };
      sources.push(source);
      return source;
    });
    sources[0].onmessage?.(new MessageEvent("message", { data: ended(outcome, 5) }));
    assert.deepEqual(calls, ["s"]);
    unwatch();
    stop();
  });
}

test("the owner stream's turn.ended does not make transcript watchers read twice", () => {
  const calls: (string | undefined)[] = [];
  const unwatch = watchTranscripts(session => calls.push(session));
  const sources: EventConnection[] = [];
  const stop = stream(undefined, () => 0, () => {}, () => {
    const source: EventConnection = { onmessage: null, onerror: null, addEventListener() {}, close() {} };
    sources.push(source);
    return source;
  });
  sources[0].onmessage?.(new MessageEvent("message", { data: ended("completed", 5) }));
  assert.deepEqual(calls, []);
  unwatch();
  stop();
});

test("a turn.ended replayed after a reconnect makes transcript watchers read again", () => {
  const calls: (string | undefined)[] = [];
  const unwatch = watchTranscripts(session => calls.push(session));
  const sources: EventConnection[] = [];
  let reconnect = () => {};
  const stop = stream("room", () => 0, () => {}, () => {
    const source: EventConnection = { onmessage: null, onerror: null, addEventListener() {}, close() {} };
    sources.push(source);
    return source;
  }, cb => { reconnect = cb; return () => {}; });
  sources[0].onerror?.(new Event("error"));
  reconnect();
  sources[1].onmessage?.(new MessageEvent("message", { data: ended("completed", 7) }));
  assert.deepEqual(calls, ["s"]);
  unwatch();
  stop();
});

for (const failBeforeHello of [false, true]) test(`opened marks retries, including failure before hello: ${failBeforeHello}`, () => {
  const hellos: ((event: MessageEvent<string>) => void)[] = [];
  const sources: EventConnection[] = [];
  let reconnect = () => {};
  const opens: (boolean | undefined)[] = [];
  const stop = stream("room", () => 0, () => {}, () => {
    const source: EventConnection = { onmessage: null, onerror: null, addEventListener(type, cb) { if (type === "hello") hellos.push(cb); }, close() {} };
    sources.push(source);
    return source;
  }, cb => { reconnect = cb; return () => {}; }, retry => { opens.push(retry); });
  if (!failBeforeHello) hellos[0](new MessageEvent("hello", { data: '{"build":"b"}' }));
  assert.deepEqual(opens, failBeforeHello ? [] : [false]);
  sources[0].onerror?.(new Event("error"));
  reconnect();
  hellos[1](new MessageEvent("hello", { data: '{"build":"b"}' }));
  assert.deepEqual(opens, failBeforeHello ? [true] : [false, true]);
  stop();
});

test("reconnects after the last applied event", () => {
  const urls: string[] = [];
  const connections: EventConnection[] = [];
  let scheduled = () => {};
  let delay = 0;
  let seq = 0;
  const stop = stream("room", () => seq, event => { seq = event.seq; }, url => {
    urls.push(url);
    const source: EventConnection = { onmessage: null, onerror: null, addEventListener() {}, close() {} };
    connections.push(source);
    return source;
  }, (callback, ms) => { scheduled = callback; delay = ms; return () => {}; });
  connections[0].onmessage?.(new MessageEvent("message", { data: JSON.stringify({
    seq: 7, id: "e", at: "", from: { kind: "owner" },
    type: "session.closed", body: { session: "s" },
  }) }));
  connections[0].onerror?.(new Event("error"));
  assert.equal(delay, 500);
  scheduled();
  assert.equal(new URL(urls[1], "http://localhost").searchParams.get("after"), "7");
  stop();
});


test("a room turn start invalidates the open transcript before its first append", () => {
  const calls: (string | undefined)[] = [];
  const unwatch = watchTranscripts(session => calls.push(session));
  const sources: EventConnection[] = [];
  const stop = stream("room", () => 0, () => {}, () => {
    const source: EventConnection = { onmessage: null, onerror: null, addEventListener() {}, close() {} };
    sources.push(source);
    return source;
  });
  sources[0].onmessage?.(new MessageEvent("message", { data: JSON.stringify({
    seq: 5, id: "e", at: "", from: { kind: "engine" }, type: "turn.started",
    body: { session: "s", turn: "t", inputs: [] },
  }) }));
  assert.deepEqual(calls, ["s"]);
  unwatch(); stop();
});

for (const room of [undefined, "room"]) test(`hello resets outage backoff on an idle ${room ?? "owner"} stream`, () => {
  const sources: EventConnection[] = [];
  const hellos: ((event: MessageEvent<string>) => void)[] = [];
  const delays: number[] = [];
  let retry = () => {};
  const stop = stream(room, () => 7, () => {}, () => {
    const source: EventConnection = {
      onmessage: null, onerror: null,
      addEventListener(type, listener) { if (type === "hello") hellos.push(listener); }, close() {},
    };
    sources.push(source);
    return source;
  }, (callback, delay) => { retry = callback; delays.push(delay); return () => {}; });
  // Repeated failed attempts cap at 10 seconds.
  for (let i = 0; i < 7; i++) {
    sources.at(-1)!.onerror?.(new Event("error"));
    retry();
  }
  assert.deepEqual(delays, [500, 1000, 2000, 4000, 8000, 10000, 10000]);
  // Server recovery sends hello before replay, even if no event was missed.
  hellos.at(-1)!(new MessageEvent("hello", { data: '{"build":"b"}' }));
  sources.at(-1)!.onerror?.(new Event("error"));
  assert.equal(delays.at(-1), 500);
  stop();
});

for (const advertised of [true, false]) test(`stall watchdog compatibility: advertised=${advertised}`, () => {
  const states: boolean[] = [];
  watchStreamState(up => states.push(up));
  let now = 0;
  const tasks = new Set<{ at: number; cb: () => void }>();
  const advance = (ms: number) => {
    const end = now + ms;
    while (true) {
      const task = [...tasks].filter(t => t.at <= end).sort((a, b) => a.at - b.at)[0];
      if (!task) break;
      tasks.delete(task); now = task.at; task.cb();
    }
    now = end;
  };
  const sources: { source: EventConnection; listeners: Map<string, (e: MessageEvent<string>) => void>; closed: boolean }[] = [];
  let seq = 0;
  const urls: string[] = [];
  const stop = stream(undefined, () => seq, e => { seq = e.seq; }, url => {
    urls.push(url);
    const listeners = new Map<string, (e: MessageEvent<string>) => void>();
    const entry = { source: {} as EventConnection, listeners, closed: false };
    entry.source = { onmessage: null, onerror: null, addEventListener(type, cb) { listeners.set(type, cb); }, close() { entry.closed = true; } };
    sources.push(entry); return entry.source;
  }, (cb, ms) => { const task = { at: now + ms, cb }; tasks.add(task); return () => { tasks.delete(task); }; });
  const first = sources[0];
  const send = (type: string, data: string) => first.listeners.get(type)?.(new MessageEvent(type, { data }));
  send('hello', JSON.stringify({ build: 'b', ...(advertised ? { heartbeatMs: 15000 } : {}) }));
  if (!advertised) {
    advance(100000); assert.equal(first.closed, false); assert.equal(tasks.size, 0);
  } else {
    for (let i = 0; i < 5; i++) { advance(15000); send('heartbeat', '{}'); }
    assert.equal(first.closed, false);
    advance(30000);
    first.source.onmessage?.(new MessageEvent('message', { data: ended('completed', 7) }));
    advance(34999); assert.equal(first.closed, false);
    advance(1); assert.equal(first.closed, true);
    assert.deepEqual(states, [true, false]);
    first.source.onerror?.(new Event('error')); // must not schedule twice
    advance(500); assert.equal(sources.length, 2);
    assert.equal(new URL(urls[1], 'http://localhost').searchParams.get('after'), '7');
    send('hello', '{"build":"stale","heartbeatMs":15000}');
    first.source.onmessage?.(new MessageEvent('message', { data: ended('completed', 99) }));
    first.source.onerror?.(new Event('error'));
    assert.equal(seq, 7); assert.equal(sources[1].closed, false); assert.equal(tasks.size, 0);
    sources[1].listeners.get('hello')?.(new MessageEvent('hello', { data: '{"build":"b","heartbeatMs":15000}' }));
    assert.equal(tasks.size, 1);
  }
  stop(); assert.equal(tasks.size, 0); advance(100000); assert.equal(sources.length, advertised ? 2 : 1);
});
