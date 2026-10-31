import { test } from "node:test";
import assert from "node:assert/strict";
import { RoomStore } from "./room.svelte.ts";
import { EventWindow, PAGE, WINDOW, dmOf } from "./window.svelte.ts";
import { feedRows } from "./feed.ts";
import type { Conversation, Event, EventBody, EventsListRequest, PostRequest, SessionSummary } from "./contract.gen.ts";

function event(seq: number, body: EventBody): Event {
  return { seq, id: String(seq), at: "", from: { kind: "owner" }, ...body };
}
const feed: Conversation = { kind: "feed" };
const dm: Conversation = { kind: "dm", session: "s" };
const usage = { model: "m", inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 };

test("merges sorted events and replaces a pending post", async () => {
  const window = new EventWindow("r", feed);
  let resolve!: (response: Response) => void;
  const original = globalThis.fetch;
  globalThis.fetch = () => new Promise<Response>(done => { resolve = done; });
  try {
    const sending = window.post({ room: "r", text: "hello" });
    assert.equal(window.pendingPosts.size, 1);
    const post = event(2, { type: "post.created", body: { text: "hello", home: feed } });
    window.apply(post);
    window.apply(event(1, { type: "session.started", body: { session: "s", name: "Agent", profile: "p" } }));
    window.apply(post);
    resolve(new Response(JSON.stringify({ seq: 2, event: "2" })));
    await sending;
    assert.deepEqual(window.seqs, [1, 2]);
    assert.equal(window.pendingPosts.size, 0);
    assert.equal(window.posts().length, 1);
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

// A fake engine with feed posts 1..count. It honors the events.list home filter and records each range.
function engine(count: number, sessions: SessionSummary[] = []) {
  const requests: string[] = [];
  const state = { down: false };
  const posts = Array.from({ length: count }, (_, i) => event(i + 1, { type: "post.created", body: { text: String(i + 1), home: feed } }));
  const original = globalThis.fetch;
  const reply = (body: object, status = 200) => new Response(JSON.stringify(body), { status });
  globalThis.fetch = async (input, init) => {
    const method = String(input).replace("/v1/", "");
    if (method === "post") {
      const request: PostRequest = JSON.parse(String(init?.body));
      posts.push(event(posts.length + 1, { type: "post.created", body: { text: request.text, home: request.home ?? feed } }));
      return reply({ seq: posts.length, event: String(posts.length) });
    }
    if (method === "session.list") return reply({ sessions, seq: posts.length });
    if (method !== "events.list") return reply({});
    if (state.down) return reply({ code: "internal", message: "down" }, 500);
    const request: EventsListRequest = JSON.parse(String(init?.body));
    const home = request.home;
    const matching = home ? posts.filter(post => dmOf(post) === home.session) : posts;
    if (request.beforeSeq) {
      requests.push(`before ${request.beforeSeq}`);
      return reply({ events: matching.filter(post => post.seq < Number(request.beforeSeq)).slice(-request.limit), next: 0 });
    }
    requests.push(`after ${request.afterSeq}`);
    return reply({ events: matching.filter(post => post.seq > request.afterSeq).slice(0, request.limit), next: 0 });
  };
  return { requests, posts, state, restore: () => { globalThis.fetch = original; } };
}

async function opened(home: Conversation = feed): Promise<EventWindow> {
  const window = new EventWindow("r", home);
  await window.open();
  return window;
}

test("releases the far end of the window and refetches it by the boundary seq", async () => {
  const server = engine(1500);
  try {
    const window = await opened();
    for (let i = 0; i < 5; i++) await window.loadOlder();
    assert.equal(window.seqs.length, WINDOW);
    assert.deepEqual([window.oldest, window.newest], [301, 1300]);
    assert.equal(window.atPresent, false);
    await window.loadNewer();
    await window.loadNewer();
    assert.deepEqual(server.requests.slice(-2), ["after 1300", "after 1500"]);
    assert.deepEqual([window.oldest, window.newest, window.atPresent], [501, 1500, true]);
  } finally { server.restore(); }
});

test("holds live events while the window is away from the present", async () => {
  const server = engine(1500);
  try {
    const window = await opened();
    for (let i = 0; i < 5; i++) await window.loadOlder();
    const post = event(1501, { type: "post.created", body: { text: "new", home: feed } });
    server.posts.push(post);
    window.apply(post);
    assert.equal(window.events.has(1501), false);
    await window.jumpToPresent();
    assert.deepEqual([window.newest, window.atPresent], [1501, true]);
  } finally { server.restore(); }
});

test("a post sent while an older page loads still brings the window to the present", async () => {
  const server = engine(1500);
  try {
    const window = await opened();
    for (let i = 0; i < 4; i++) await window.loadOlder();
    const older = window.loadOlder();
    await window.post({ room: "r", text: "sent" });
    await older;
    assert.deepEqual([window.newest, window.atPresent, window.pendingPosts.size], [1501, true, 0]);
    assert.equal(window.posts().at(-1)?.body.text, "sent");
  } finally { server.restore(); }
});

test("a failed jump keeps the sent post and shows the load error", async () => {
  const server = engine(1500);
  try {
    const window = await opened();
    for (let i = 0; i < 5; i++) await window.loadOlder();
    server.state.down = true;
    await window.post({ room: "r", text: "sent" });
    assert.deepEqual([window.atPresent, window.pendingPosts.size], [false, 1]);
    assert.notEqual(window.error, undefined);
    server.state.down = false;
    await window.jumpToPresent();
    assert.deepEqual([window.newest, window.pendingPosts.size, window.error], [1501, 0, undefined]);
  } finally { server.restore(); }
});

test("fill loads older pages for sparse rows but never leaves the present", async () => {
  const server = engine(5000);
  // A busy DM leaves the feed two posts, at 1200 and 4800.
  server.posts.forEach(({ seq }, i) => {
    if (seq !== 1200 && seq !== 4800) server.posts[i] = event(seq, { type: "post.created", body: { text: "dm", home: dm } });
  });
  try {
    const window = await opened();
    await window.fill(async () => window.posts().length > 0);
    assert.deepEqual([window.oldest, window.atPresent], [4601, true]);
    await window.fill(async () => false);
    assert.deepEqual([window.oldest, window.newest, window.atPresent, window.hasOlder], [4001, 5000, true, true]);
    assert.equal(server.requests.filter(request => request.startsWith("before")).length, 5);
  } finally { server.restore(); }
});

test("a DM window reads and keeps only the events whose home is that DM", async () => {
  const server = engine(1500);
  server.posts.push(event(1501, { type: "post.created", body: { text: "ask", home: dm } }));
  try {
    const window = await opened(dm);
    assert.deepEqual(window.seqs, [1501]);
    window.apply(event(1502, { type: "post.created", body: { text: "elsewhere", home: feed } }));
    window.apply(event(1503, { type: "turn.started", body: { session: "s", turn: "t", inputs: [] } }));
    assert.deepEqual(window.seqs, [1501]);
  } finally { server.restore(); }
});

test("session.list gives running turns and failures, and a live DM answer lands in the DM, not the feed", async () => {
  const listed = { name: "Agent", parent: "", profile: "p", box: "b", boxState: "ready", goal: "" } as const;
  const server = engine(3, [{ id: "s", ...listed, turn: "t" }, { id: "x", ...listed, name: "Other", lastOutcome: "failed" }]);
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    assert.deepEqual([room.turns.get("s"), room.failed.has("x")], ["t", true]);
    const feedWindow = room.window(feed);
    const dmWindow = room.window(dm);
    await Promise.all([feedWindow.jumpToPresent(), dmWindow.jumpToPresent()]);
    assert.deepEqual(server.requests, ["before 9007199254740991", "before 9007199254740991"]);
    room.apply(event(4, { type: "turn.ended", body: { session: "s", turn: "t", outcome: "completed", final: "Answer", usage,
      replies: [{ reply: { kind: "final", target: { kind: "owner" }, home: dm } }] } }));
    assert.equal(room.turns.has("s"), false);
    assert.deepEqual(feedRows(dmWindow.ordered, dm).map(row => row.kind === "post" && row.text), ["Answer"]);
    assert.equal(feedRows(feedWindow.ordered, feed).some(row => row.kind === "post" && row.text === "Answer"), false);
  } finally { server.restore(); }
});
