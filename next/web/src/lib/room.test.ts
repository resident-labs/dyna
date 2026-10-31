import { test } from "node:test";
import assert from "node:assert/strict";
import { PAGE, RoomStore, WINDOW } from "./room.svelte.ts";
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

// A fake engine with posts 1..count; it records each events.list range.
function engine(count: number) {
  const requests: string[] = [];
  const state = { down: false };
  const posts = Array.from({ length: count }, (_, i) => event(i + 1, { type: "post.created", body: { text: String(i + 1), home: { kind: "feed" } } }));
  const original = globalThis.fetch;
  globalThis.fetch = async (input, init) => {
    const method = String(input).replace("/v1/", "");
    const request: { afterSeq?: number; beforeSeq?: number; limit?: number } = JSON.parse(String(init?.body));
    if (method === "post") {
      posts.push(event(posts.length + 1, { type: "post.created", body: { text: "sent", home: { kind: "feed" } } }));
      return new Response(JSON.stringify({ seq: posts.length, event: String(posts.length) }));
    }
    if (method !== "events.list") return new Response(JSON.stringify({}));
    if (state.down) return new Response(JSON.stringify({ code: "internal", message: "down" }), { status: 500 });
    const limit = request.limit ?? PAGE;
    if (request.beforeSeq) {
      requests.push(`before ${request.beforeSeq}`);
      const before = posts.filter(post => post.seq < Number(request.beforeSeq));
      return new Response(JSON.stringify({ events: before.slice(-limit), next: 0 }));
    }
    requests.push(`after ${request.afterSeq}`);
    return new Response(JSON.stringify({ events: posts.filter(post => post.seq > Number(request.afterSeq)).slice(0, limit), next: 0 }));
  };
  return { requests, posts, state, restore: () => { globalThis.fetch = original; } };
}

test("releases the far end of the window and refetches it by the boundary seq", async () => {
  const server = engine(1500);
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    for (let i = 0; i < 5; i++) await room.loadOlder();
    assert.equal(room.seqs.length, WINDOW);
    assert.deepEqual([room.oldest, room.newest], [301, 1300]);
    assert.equal(room.atPresent, false);
    await room.loadNewer();
    await room.loadNewer();
    assert.deepEqual(server.requests.slice(-2), ["after 1300", "after 1500"]);
    assert.deepEqual([room.oldest, room.newest, room.atPresent], [501, 1500, true]);
  } finally { server.restore(); }
});

test("holds live events while the window is away from the present", async () => {
  const server = engine(1500);
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    for (let i = 0; i < 5; i++) await room.loadOlder();
    room.apply(event(1501, { type: "session.started", body: { session: "s", name: "Agent", profile: "p" } }));
    assert.equal(room.events.has(1501), false);
    assert.equal(room.sessions.get("s")?.name, "Agent");
    server.posts.push(event(1501, { type: "post.created", body: { text: "new", home: { kind: "feed" } } }));
    await room.jumpToPresent();
    assert.deepEqual([room.newest, room.atPresent], [1501, true]);
  } finally { server.restore(); }
});

test("a post sent while an older page loads still brings the window to the present", async () => {
  const server = engine(1500);
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    for (let i = 0; i < 4; i++) await room.loadOlder();
    const older = room.loadOlder();
    await room.post({ room: "r", text: "sent" });
    await older;
    assert.deepEqual([room.newest, room.atPresent, room.pendingPosts.size], [1501, true, 0]);
    assert.equal(room.posts({ kind: "feed" }).at(-1)?.body.text, "sent");
  } finally { server.restore(); }
});

test("a failed jump keeps the sent post and shows the load error", async () => {
  const server = engine(1500);
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    for (let i = 0; i < 5; i++) await room.loadOlder();
    server.state.down = true;
    await room.post({ room: "r", text: "sent" });
    assert.deepEqual([room.atPresent, room.pendingPosts.size], [false, 1]);
    assert.notEqual(room.error, undefined);
    server.state.down = false;
    await room.jumpToPresent();
    assert.deepEqual([room.newest, room.pendingPosts.size, room.error], [1501, 0, undefined]);
  } finally { server.restore(); }
});
