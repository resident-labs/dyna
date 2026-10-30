import { test } from "node:test";
import assert from "node:assert/strict";
import { RoomStore } from "./room.svelte.ts";
import type { Event, EventBody } from "./contract.gen.ts";

function event(seq: number, body: EventBody): Event {
  return { seq, id: String(seq), at: "", from: { kind: "owner" }, ...body };
}

test("merges sorted events and replaces a pending post", async () => {
  const room = new RoomStore("r");
  let resolve!: (response: Response) => void;
  const original = globalThis.fetch;
  globalThis.fetch = () => new Promise<Response>(done => { resolve = done; });
  try {
    const sending = room.post({ room: "r", text: "hello" });
    assert.equal(room.pendingPosts.size, 1);
    const post = event(2, { type: "post.created", body: { text: "hello", home: { kind: "feed" } } });
    room.apply(post);
    room.apply(event(1, { type: "session.started", body: { session: "s", name: "Agent", profile: "p" } }));
    room.apply(post);
    resolve(new Response(JSON.stringify({ seq: 2, event: "2" })));
    await sending;
    assert.deepEqual(room.seqs, [1, 2]);
    assert.equal(room.pendingPosts.size, 0);
    assert.equal(room.posts({ kind: "feed" }).length, 1);
    assert.equal(room.sessions.get("s")?.name, "Agent");
  } finally { globalThis.fetch = original; }
});

test("folds sessions, box, turns and pending operations", () => {
  const room = new RoomStore("r");
  room.apply(event(1, { type: "session.started", body: { session: "p", name: "Parent", profile: "p" } }));
  room.apply(event(2, { type: "session.started", body: { session: "s", name: "Child", parent: "p", profile: "p" } }));
  room.apply(event(3, { type: "box.changed", body: { box: "b", session: "s", from: "pending", to: "ready" } }));
  room.apply(event(4, { type: "turn.started", body: { session: "s", turn: "t", inputs: [] } }));
  room.apply(event(5, { type: "op.requested", body: { id: "o", kind: "test", by: { kind: "owner" }, class: "owner", card: { title: "Test", target: { kind: "room", name: "r" } }, expires: "" } }));
  assert.equal(room.sessions.get("s")?.parent, "p");
  assert.equal(room.boxes.get("s"), "ready");
  assert.equal(room.turns.get("s"), "t");
  assert.equal(room.operations.size, 1);
  room.apply(event(6, { type: "op.decided", body: { id: "o", state: "denied", decision: { by: { kind: "owner" }, approved: false, at: "" } } }));
  room.apply(event(7, { type: "session.closed", body: { session: "s" } }));
  assert.equal(room.operations.size, 0);
  assert.equal(room.sessions.has("s"), false);
});
