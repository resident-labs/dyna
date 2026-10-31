import { test } from "node:test";
import assert from "node:assert/strict";
import { stream, watchHello, type EventConnection } from "./stream.ts";
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
    onmessage: null, onerror: null, addEventListener(_type, callback) { listener = callback; }, close() {},
  }));
  listener(new MessageEvent("hello", { data: '{"build":"b"}' }));
  assert.equal(build, "b");
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
