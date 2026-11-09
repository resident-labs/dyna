import { test } from "node:test";
import assert from "node:assert/strict";
import { PageEvents } from "./page-events.ts";
import { stream, type StreamOpened } from "./stream.ts";
import type { Event } from "./contract.gen.ts";

function fixture() {
  const connections: { room?: string; after: () => number; attentionAfter?: () => number; apply: (event: Event) => void; opened: StreamOpened; closed: boolean }[] = [];
  let active = 0, maximum = 0;
  const transport: typeof stream = (room, after, apply, _factory, _schedule, opened = () => {}, _lifecycle, attentionAfter) => {
    const connection = { room, after, attentionAfter, apply, opened, closed: false };
    connections.push(connection); maximum = Math.max(maximum, ++active);
    return () => { if (!connection.closed) { connection.closed = true; active--; } };
  };
  return { page: new PageEvents(transport), connections, latest: () => connections.at(-1)!, active: () => active, maximum: () => maximum };
}
const hello = (roomUnion?: boolean) => ({ build: "fixture", ...(roomUnion === undefined ? {} : { roomUnion }) });
const event = (seq: number, room = "r"): Event => ({ seq, room, id: `e${seq}`, at: "", from: { kind: "engine" }, type: "session.closed", body: { session: "s" } });

for (const [ownerStart, roomStart] of [[100, 10], [10, 100]]) {
  test(`union preserves separate snapshot cursors ${ownerStart}/${roomStart} and one physical stream`, () => {
    const f = fixture(); let ownerHead = ownerStart, roomHead = roomStart;
    const ownerEvents: number[] = [], roomEvents: number[] = [];
    f.page.connect(undefined, () => ownerHead, e => { if (e.seq > ownerHead) { ownerHead = e.seq; ownerEvents.push(e.seq); } });
    f.page.connect("r", () => roomHead, e => { if (e.seq > roomHead) { roomHead = e.seq; roomEvents.push(e.seq); } });
    // The unknown-capability connection remains owner-only until its first hello.
    assert.equal(f.latest().room, undefined);
    f.latest().opened(false, hello(true));
    const union = f.latest(); union.opened(false, hello(true));
    assert.equal(union.room, "r");
    assert.equal(union.after(), roomStart);
    assert.equal(union.attentionAfter?.(), ownerStart);
    union.apply(event(50)); union.apply(event(110, "other")); union.apply(event(120));
    assert.deepEqual(ownerEvents, ownerStart === 100 ? [110, 120] : [50, 110, 120]);
    assert.deepEqual(roomEvents, roomStart === 100 ? [120] : [50, 120]);
    assert.equal(union.after(), 120); assert.equal(union.attentionAfter?.(), 120);
    assert.equal(f.active(), 1); assert.equal(f.maximum(), 1);
    f.page.close(); assert.equal(f.active(), 0);
  });
}

test("hello repairs keep each consumer's reconnect identity across transport replacements", () => {
  const f = fixture(), owner: boolean[] = [], room: boolean[] = [];
  f.page.connect(undefined, () => 1, () => {}, undefined, undefined, again => owner.push(!!again));
  f.latest().opened(false, hello(true));
  f.page.connect("r", () => 2, () => {}, undefined, undefined, again => room.push(!!again));
  f.latest().opened(false, hello(true)); f.latest().opened(true, hello(true));
  assert.deepEqual(owner, [false, true, true]); assert.deepEqual(room, [false, true]);
  assert.equal(f.maximum(), 1); f.page.close();
});

test("a room can finish its snapshots before attention without losing its hello repair", () => {
  const f = fixture(), opened: boolean[] = [];
  f.page.connect("r", () => 3, () => {}, undefined, undefined, again => opened.push(!!again));
  f.latest().opened(false, hello(true));
  f.page.connect(undefined, () => 7, () => {});
  f.latest().opened(false, hello(true));
  assert.deepEqual(opened, [false, true]); assert.equal(f.maximum(), 1); f.page.close();
});

test("room switches, stale callbacks and late old disposers cannot change the new subscription", () => {
  const f = fixture(), seen: string[] = [];
  f.page.connect(undefined, () => 10, e => seen.push(`owner:${e.room}`)); f.latest().opened(false, hello(true));
  const oldStop = f.page.connect("r", () => 5, e => seen.push(`old:${e.room}`));
  const old = f.latest(); old.opened(false, hello(true));
  const stop = f.page.connect("next", () => 8, e => seen.push(`new:${e.room}`));
  const current = f.latest(); current.opened(false, hello(true));
  oldStop(); old.apply(event(11)); old.opened(true, hello(false));
  assert.equal(f.latest(), current);
  current.apply(event(12, "r")); current.apply(event(13, "next"));
  current.apply({ seq: 0, id: "read", room: "next", at: "", from: { kind: "owner" }, type: "conversation.read", body: { room: "next", conversation: { kind: "feed" }, seq: 13 } });
  assert.deepEqual(seen, ["owner:r", "owner:next", "new:next", "owner:next", "new:next"]);
  stop(); assert.equal(f.latest().room, undefined); assert.equal(f.active(), 1);
  f.page.close(); current.apply(event(14, "next")); assert.equal(seen.length, 5); assert.equal(f.active(), 0);
});

for (const capability of [undefined, false]) {
  test(`old server capability ${capability} retains legacy streams and can upgrade/downgrade`, () => {
    const f = fixture(), seen: string[] = [];
    f.page.connect(undefined, () => 100, () => seen.push("owner"));
    const stopRoom = f.page.connect("r", () => 10, () => seen.push("room"));
    f.latest().opened(false, hello(capability));
    const legacy = f.connections.filter(c => !c.closed);
    assert.equal(legacy.length, 2);
    for (const source of legacy) source.opened(false, hello(capability));
    // Legacy room transports already own their scope, including older transient envelopes.
    legacy.find(c => c.room === "r")!.apply({ ...event(11), room: undefined });
    assert.deepEqual(seen, ["room"]);
    legacy[0].opened(true, hello(true));
    assert.equal(f.active(), 1); assert.ok(f.latest().attentionAfter);
    f.latest().opened(true, hello(false));
    assert.equal(f.active(), 2);
    stopRoom(); assert.equal(f.active(), 1); f.page.close(); assert.equal(f.active(), 0);
  });
}

test("disposal removes both logical consumers and ignores registrations after close", () => {
  const f = fixture(); let events = 0;
  const stop = f.page.connect(undefined, () => 0, () => events++);
  const stale = f.latest(); f.page.close(); f.page.close(); stop();
  f.page.connect("r", () => 0, () => events++);
  stale.apply(event(1)); stale.opened(false, hello(true));
  assert.equal(events, 0); assert.equal(f.active(), 0); assert.equal(f.connections.length, 1);
});

test("a consumer can dispose synchronously during hello without leaking the returned transport", () => {
  let active = 0;
  const transport: typeof stream = (_room, _after, _apply, _factory, _schedule, opened = () => {}) => {
    active++; opened(false, hello(true)); return () => active--;
  };
  const page = new PageEvents(transport);
  page.connect(undefined, () => 0, () => {}, undefined, undefined, () => page.close());
  assert.equal(active, 0);
});
