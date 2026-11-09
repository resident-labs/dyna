import { test } from "node:test";
import assert from "node:assert/strict";
import { stream, watchStreamState, watchTranscripts, type EventConnection } from "./stream.ts";
import type { StreamHello } from "./contract.gen.ts";

test("combined transport carries both cursors, owner state and selected-room transcript boundaries", () => {
  let url = "";
  const listeners = new Map<string, (event: MessageEvent<string>) => void>();
  const source: EventConnection = { onmessage: null, onerror: null, addEventListener(type, listener) { listeners.set(type, listener); }, close() {} };
  const states: boolean[] = [], invalidated: (string | undefined)[] = [];
  watchStreamState(up => states.push(up));
  const unwatch = watchTranscripts(session => invalidated.push(session));
  let hello: StreamHello | undefined;
  const stop = stream("r", () => 5, () => {}, value => { url = value; return source; }, () => () => {}, (_again, value) => { hello = value; }, undefined, () => 100);
  const query = new URL(url, "http://fixture").searchParams;
  assert.equal(query.get("room"), "r"); assert.equal(query.get("after"), "100"); assert.equal(query.get("roomAfter"), "5");
  listeners.get("hello")!(new MessageEvent("hello", { data: '{"build":"b","roomUnion":true}' }));
  assert.equal(hello?.roomUnion, true); assert.deepEqual(states, [true]);
  assert.deepEqual(invalidated, [undefined]);
  for (const room of ["other", "r"]) source.onmessage!(new MessageEvent("message", { data: JSON.stringify({ seq: 101, id: "e", room, at: "", from: { kind: "engine" }, type: "turn.started", body: { turn: "t", session: "s", inputs: [] } }) }));
  assert.deepEqual(invalidated, [undefined, "s"]);
  source.onerror!(new Event("error")); assert.deepEqual(states, [true, false]);
  stop(); unwatch();
});

test("capability is accepted only as a boolean at the hello boundary", () => {
  for (const value of [true, false, "true", 1, null]) {
    let receive: (event: MessageEvent<string>) => void = () => {};
    let hello: StreamHello | undefined;
    const stop = stream(undefined, () => 0, () => {}, () => ({ onmessage: null, onerror: null,
      addEventListener(type, listener) { if (type === "hello") receive = listener; }, close() {} }),
    () => () => {}, (_again, next) => { hello = next; });
    receive(new MessageEvent("hello", { data: JSON.stringify({ build: "b", roomUnion: value }) }));
    assert.equal(hello?.roomUnion, typeof value === "boolean" ? value : undefined);
    stop();
  }
});
