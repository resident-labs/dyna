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

test("a pending post gives way to its event in either order, and a failed one stays until a retry sends it", async () => {
  const window = new EventWindow("r", feed);
  let resolve!: (response: Response) => void;
  const original = globalThis.fetch;
  globalThis.fetch = () => new Promise<Response>(done => { resolve = done; });
  const rows = () => feedRows(window.ordered, feed, [...window.pendingPosts.values()].map(({ id, at, failed, request }) =>
    ({ id, at, failed, text: request.text, attachments: [], to: [] }))).map(row => row.kind === "post" ? `${row.key} ${row.text}` : row.kind);
  try {
    // The event arrives before the call returns.
    let sending = window.post({ room: "r", text: "hello" });
    assert.deepEqual(rows(), ["n1 hello"]);
    const post = event(2, { type: "post.created", body: { text: "hello", home: feed } });
    window.apply(post);
    window.apply(event(1, { type: "session.started", body: { session: "s", name: "Agent", profile: "p" } }));
    window.apply(post);
    resolve(new Response(JSON.stringify({ seq: 2, event: "2" })));
    await sending;
    assert.deepEqual([window.seqs, rows()], [[1, 2], ["work", "p2 hello"]]);
    // The call returns first; a post with the same text is not its event.
    sending = window.post({ room: "r", text: "hello" });
    resolve(new Response(JSON.stringify({ seq: 4, event: "4" })));
    await sending;
    window.apply(event(3, { type: "post.created", body: { text: "hello", home: feed } }));
    assert.deepEqual(rows(), ["work", "p2 hello", "p3 hello", "n2 hello"]);
    window.apply(event(4, { type: "post.created", body: { text: "hello", home: feed } }));
    assert.deepEqual(rows(), ["work", "p2 hello", "p3 hello", "p4 hello"]);
    // A failed call keeps the row and hands the post to the app; a retry takes it back and sends the same body once.
    const kept = new Set<PostRequest>();
    window.onunsent = request => kept.add(request);
    window.onsettled = request => kept.delete(request);
    const bodies: unknown[] = [];
    globalThis.fetch = (_, init) => { bodies.push(init?.body); return Promise.reject(new Error("offline")); };
    await window.post({ room: "r", text: "later" });
    assert.deepEqual([[...window.pendingPosts.values()].map(({ failed, seq }) => [failed, seq]), kept.size], [[[true, undefined]], 1]);
    globalThis.fetch = (_, init) => { bodies.push(init?.body); return Promise.resolve(new Response(JSON.stringify({ seq: 5, event: "5" }))); };
    // A second Retry while the first sends, as from a double click, sends nothing more.
    await Promise.all([window.retry(3), window.retry(3)]);
    assert.deepEqual([[...window.pendingPosts.values()].map(({ failed, seq }) => [failed, seq]), kept.size], [[[false, 5]], 0]);
    assert.deepEqual([bodies.length, bodies[0] === bodies[1]], [2, true]);
    window.apply(event(5, { type: "post.created", body: { text: "later", home: feed } }));
    assert.deepEqual(rows().at(-1), "p5 later");
    assert.equal(window.pendingPosts.size, 0);
    // A post the app kept shows as failed in a new window, and Discard gives it up.
    const request = { room: "r", text: "kept" };
    kept.add(request);
    window.discard(window.unsent(request));
    assert.deepEqual([window.pendingPosts.size, kept.size], [0, 0]);
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
  assert.equal(room.waiting, 1);
  room.apply(event(6, { type: "op.decided", body: { id: "o", state: "denied", decision: { by: { kind: "owner" }, approved: false, at: "" } } }));
  room.apply(event(7, { type: "session.closed", body: { session: "s" } }));
  assert.equal(room.waiting, 0);
  assert.equal(room.sessions.has("s"), false);
});

test("the waiting count holds pending owner-class operations from op.list and op events after the baseline", async () => {
  const card = { title: "Open a pull request", target: { kind: "repo", name: "web" } } as const;
  const listed = { id: "a", kind: "pr.open", room: "r", by: { kind: "session", session: "s" }, class: "owner", card, args: { title: "", body: "", base: "main", draft: false },
    state: "pending", created: "", expires: "" } as const;
  const requested = (seq: number, id: string, level: "agent" | "owner") => event(seq, { type: "op.requested", body: { id, kind: "pr.open", by: listed.by, class: level, card, expires: "" } });
  const original = globalThis.fetch;
  globalThis.fetch = async input => new Response(JSON.stringify(String(input).endsWith("op.list") ? { operations: [listed] } : { sessions: [], seq: 10 }));
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    assert.equal(room.waiting, 1);
    const decided = (seq: number, id: string) => event(seq, { type: "op.decided", body: { id, state: "approved", decision: { by: { kind: "owner" }, approved: true, at: "" } } });
    room.apply(decided(10, "a"));
    room.apply(requested(11, "b", "agent"));
    room.apply(requested(12, "c", "owner"));
    assert.deepEqual([room.waiting, room.requests.get("b")?.state], [2, "pending"]);
    room.apply(decided(13, "a"));
    room.apply(event(14, { type: "op.ran", body: { id: "a", state: "failed", result: { exitCode: 1, error: "push failed" } } }));
    assert.deepEqual([room.waiting, room.requests.get("a")?.state, room.requests.get("a")?.result?.error], [1, "failed", "push failed"]);
  } finally { globalThis.fetch = original; }
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
  const listed = { lastRead: 0, name: "Agent", parent: "", profile: "p", box: "b", boxState: "ready", computer: "stopped", goal: "" } as const;
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
    room.apply(event(5, { type: "turn.ended", body: { session: "x", turn: "u", outcome: "stopped", usage } }));
    assert.equal(room.failed.has("x"), true);
    assert.deepEqual(feedRows(dmWindow.ordered, dm).map(row => row.kind === "post" && row.text), ["Answer"]);
    assert.equal(feedRows(feedWindow.ordered, feed).some(row => row.kind === "post" && row.text === "Answer"), false);
  } finally { server.restore(); }
});

test("session.list and later events set and clear the running computers", async () => {
  const listed = { lastRead: 0, name: "Agent", parent: "", profile: "p", box: "b", boxState: "ready", goal: "" } as const;
  const server = engine(3, [{ id: "s", ...listed, computer: "running" }, { id: "x", ...listed, computer: "stopped" }]);
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    assert.deepEqual([...room.computers], ["s"]);
    // Events at or before the list's seq are already in it.
    room.apply(event(3, { type: "computer.changed", body: { session: "x", to: "running" } }));
    assert.equal(room.computers.has("x"), false);
    room.apply(event(4, { type: "computer.changed", body: { session: "x", to: "running" } }));
    room.apply(event(5, { type: "computer.changed", body: { session: "s", to: "stopped" } }));
    assert.deepEqual([...room.computers], ["x"]);
    room.apply(event(6, { type: "box.changed", body: { box: "b", session: "x", from: "ready", to: "lost" } }));
    assert.equal(room.computers.size, 0);
  } finally { server.restore(); }
});

test("activity labels follow the running turn and never reach a window", async () => {
  const listed = { lastRead: 0, name: "Agent", parent: "", profile: "p", box: "b", boxState: "ready", computer: "stopped", goal: "" } as const;
  const server = engine(1, [{ id: "s", ...listed, turn: "t", activity: "read spec.md" }]);
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    const feedWindow = room.window(feed);
    await feedWindow.jumpToPresent();
    const label = (turn: string, text: string) => event(0, { type: "turn.activity", body: { session: "s", turn, label: text } });
    assert.equal(room.activity.get("s"), "read spec.md");
    room.apply(label("t", "ran go test"));
    room.apply(label("old", "stale"));
    assert.equal(room.activity.get("s"), "ran go test");
    room.apply(event(2, { type: "turn.ended", body: { session: "s", turn: "t", outcome: "completed", usage } }));
    assert.equal(room.activity.has("s"), false);
    assert.equal(feedWindow.ordered.some(item => item.type === "turn.activity"), false);
  } finally { server.restore(); }
});
