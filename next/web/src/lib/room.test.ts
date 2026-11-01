import { test } from "node:test";
import assert from "node:assert/strict";
import { RoomStore } from "./room.svelte.ts";
import { stream, type EventConnection } from "./stream.ts";
import { EventWindow, PAGE, WINDOW, dmOf } from "./window.svelte.ts";
import { feedRows as dayRows } from "./feed.ts";
import type { BoxState, Conversation, Event, EventBody, EventsListRequest, PostRequest, SessionSummary } from "./contract.gen.ts";

// These cases leave out the day rows, which feed.test.ts covers.
const feedRows = (...args: Parameters<typeof dayRows>) => dayRows(...args).filter(row => row.kind !== "day");

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
  room.apply(event(8, { type: "session.profile_changed", body: { session: "p", profile: "deep" } }));
  assert.equal(room.sessions.get("p")?.profile, "deep");
});

test("the waiting count holds pending owner-class operations from op.list and op events after the baseline", async () => {
  const card = { title: "asks to open a pull request", target: { kind: "repo", name: "web" } } as const;
  const listed = { id: "a", kind: "pr.open", room: "r", by: { kind: "session", session: "s" }, class: "owner", card, args: { title: "", body: "", base: "main", draft: false },
    state: "pending", created: "", expires: "" } as const;
  const requested = (seq: number, id: string, level: "auto" | "owner") => event(seq, { type: "op.requested", body: { id, kind: "pr.open", by: listed.by, class: level, card, expires: "" } });
  const original = globalThis.fetch;
  globalThis.fetch = async input => new Response(JSON.stringify(String(input).endsWith("op.list") ? { operations: [listed] } : { rooms: [], sessions: [], seq: 10 }));
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    assert.equal(room.waiting, 1);
    const decided = (seq: number, id: string) => event(seq, { type: "op.decided", body: { id, state: "approved", decision: { by: { kind: "owner" }, approved: true, at: "" } } });
    room.apply(decided(10, "a"));
    room.apply(requested(11, "b", "auto"));
    room.apply(requested(12, "c", "owner"));
    assert.deepEqual([room.waiting, room.requests.get("b")?.state], [2, "pending"]);
    room.apply(decided(13, "a"));
    room.apply(event(14, { type: "op.ran", body: { id: "a", state: "failed", result: { exitCode: 1, error: "push failed" } } }));
    assert.deepEqual([room.waiting, room.requests.get("a")?.state, room.requests.get("a")?.result?.error], [1, "failed", "push failed"]);
  } finally { globalThis.fetch = original; }
});

test("a question waits for the owner until it is answered or withdrawn, and marks its session as waiting", async () => {
  const listed = { id: "q1", room: "r", session: "s", text: "Which?", state: "pending", asked: "2026-01-01T00:00:00Z" };
  const original = globalThis.fetch;
  globalThis.fetch = async input => new Response(JSON.stringify(String(input).endsWith("question.list") ? { questions: [listed] } : { rooms: [], sessions: [{ id: "s", name: "s", boxState: "ready" }], seq: 10 }));
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    assert.deepEqual([room.waiting, room.status("s"), room.pendingQuestions("s").length, room.pendingQuestions("other").length], [1, "waiting", 1, 0]);
    room.apply(event(11, { type: "question.asked", body: { id: "q2", session: "s", text: "And?", meanwhile: "The tests", blocked: true } }));
    assert.equal(room.waiting, 2);
    assert.deepEqual([room.questions.get("q2")?.meanwhile, room.questions.get("q2")?.blocked], ["The tests", true]);
    room.apply(event(12, { type: "question.answered", body: { id: "q1", session: "s", question: "Which?", answer: "This" } }));
    assert.deepEqual([room.waiting, room.questions.get("q1")?.state, room.questions.get("q1")?.answer, room.questions.get("q1")?.asked], [1, "answered", "This", listed.asked]);
    room.apply(event(13, { type: "question.withdrawn", body: { id: "q2", session: "s" } }));
    assert.deepEqual([room.waiting, room.status("s")], [0, "idle"]);
  } finally { globalThis.fetch = original; }
});

test("a DM holds a session's questions, and shows an answer as the owner's post until a turn takes it", () => {
  const window = new EventWindow("r", dm);
  const answered = event(2, { type: "question.answered", body: { id: "q", session: "s", question: "Which?", answer: "This" } });
  window.apply(event(1, { type: "question.asked", body: { id: "q", session: "s", text: "Which?" } }));
  window.apply(answered);
  window.apply(event(3, { type: "question.asked", body: { id: "q2", session: "other", text: "Not here" } }));
  window.apply(event(4, { type: "question.withdrawn", body: { id: "q2", session: "other" } }));
  assert.deepEqual(window.ordered.map(item => item.seq), [1, 2]);
  assert.deepEqual(window.posts().map(post => [post.seq, post.body.text]), [[2, "Question: Which?\n\nAnswer: This"]]);
  assert.equal(dmOf(answered), "s");
});

test("a question stays out of the room feed", () => {
  const events = [event(1, { type: "question.asked", body: { id: "q", session: "s", text: "Which?" } }), event(2, { type: "question.answered", body: { id: "q", session: "s", question: "Which?", answer: "This" } }), event(3, { type: "question.withdrawn", body: { id: "q2", session: "s" } })];
  assert.deepEqual(feedRows(events, feed), []);
});

test("a pending post is replaced by the event that echoes its clientId, before its call returns", async () => {
  const window = new EventWindow("r", feed);
  const original = globalThis.fetch;
  let body = "";
  globalThis.fetch = (input, init) => { if (String(input).endsWith("post")) body = String(init?.body); return new Promise<Response>(() => {}); };
  try {
    void window.post({ room: "r", text: "hello" });
    const clientId = JSON.parse(body).clientId;
    assert.equal(typeof clientId, "string");
    window.apply(event(9, { type: "post.created", body: { text: "hello", home: feed, clientId } }));
    assert.equal(window.pendingPosts.size, 0);
    // The event arrives, then the call fails: the delivered post does not come back as unsent.
    let reject!: (error: Error) => void;
    globalThis.fetch = (input, init) => { if (String(input).endsWith("post")) body = String(init?.body); return new Promise<Response>((_, fail) => { reject = fail; }); };
    const lost = window.post({ room: "r", text: "again" });
    window.apply(event(10, { type: "post.created", body: { text: "again", home: feed, clientId: JSON.parse(body).clientId } }));
    reject(new Error("offline"));
    await lost;
    assert.equal(window.pendingPosts.size, 0);
  } finally { globalThis.fetch = original; }
});

// A fake engine with feed posts 1..count. It honors the events.list home filter and records each range.
function engine(count: number, sessions: SessionSummary[] = []) {
  const requests: string[] = [];
  const state = { down: false, feedRead: 0 };
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
    if (method === "room.list") return reply({ rooms: [{ id: "r", lastRead: state.feedRead }], seq: posts.length });
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

test("a search jump waits for an in-flight page before seeking the post", async () => {
  const server = engine(PAGE * 2 + 1);
  try {
    const window = await opened();
    const loading = window.loadOlder();
    await window.seek(1);
    await loading;
    assert.equal(window.events.has(1), true);
  } finally { server.restore(); }
});

test("a search jump reaches a newer post after the window has moved back", async () => {
  const server = engine(1500);
  try {
    const window = await opened();
    for (let i = 0; i < 5; i++) await window.loadOlder();
    assert.equal(window.events.has(1500), false);
    await window.seek(1500);
    assert.equal(window.events.has(1500), true);
  } finally { server.restore(); }
});

test("keeps the older page boundary when a live event arrives during scroll settling", async () => {
  const server = engine(1500);
  try {
    const window = await opened();
    for (let i = 0; i < 4; i++) await window.loadOlder();
    let release!: () => void, fetched!: () => void;
    const settling = new Promise<void>(resolve => { release = resolve; });
    const ready = new Promise<void>(resolve => { fetched = resolve; });
    const older = window.loadOlder(() => { fetched(); return settling; });
    await ready;
    const post = event(1501, { type: "post.created", body: { text: "new", home: feed } });
    server.posts.push(post);
    window.apply(post);
    release();
    await older;
    assert.deepEqual(window.seqs, Array.from({ length: WINDOW }, (_, i) => i + 301));
    assert.equal(window.atPresent, false);
    await window.jumpToPresent();
    assert.equal(window.newest, 1501);
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
  const listed = { lastRead: 0, name: "Agent", parent: "", profile: "p", box: "b", boxState: "ready", computer: "stopped", goal: "", startedAt: "", hadJobs: false } as const;
  const server = engine(3, [{ id: "s", ...listed, turn: "t" }, { id: "x", ...listed, name: "Other", lastOutcome: "failed" }]);
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    assert.deepEqual([room.turns.get("s"), room.failed.has("x")], ["t", true]);
    const feedWindow = room.window(feed);
    const dmWindow = room.window(dm);
    await Promise.all([feedWindow.jumpToPresent(), dmWindow.jumpToPresent()]);
    // The first read finds the seq of x's failure among the newest turn.ended events.
    assert.deepEqual(server.requests, ["before 4", "before 9007199254740991", "before 9007199254740991"]);
    room.apply(event(4, { type: "turn.ended", body: { session: "s", turn: "t", outcome: "completed", final: "Answer", usage,
      replies: [{ reply: { kind: "final", target: { kind: "owner" }, home: dm } }] } }));
    assert.equal(room.turns.has("s"), false);
    room.apply(event(5, { type: "turn.ended", body: { session: "x", turn: "u", outcome: "stopped", usage } }));
    assert.equal(room.failed.has("x"), true);
    assert.deepEqual(feedRows(dmWindow.ordered, dm).filter(row => row.kind === "post").map(row => row.text), ["Answer"]);
    assert.equal(feedRows(feedWindow.ordered, feed).some(row => row.kind === "post" && row.text === "Answer"), false);
  } finally { server.restore(); }
});

test("a failed turn marks its session until the owner reads past it in the feed or the DM, and a later failure marks it again", () => {
  const failed = (seq: number) => event(seq, { type: "turn.ended", body: { session: "s", turn: String(seq), outcome: "failed", usage } });
  const read = (conversation: Conversation, seq: number) => event(0, { type: "conversation.read", body: { room: "r", conversation, seq } });
  const room = new RoomStore("r", () => () => {});
  room.apply(failed(1));
  assert.equal(room.unseen("s"), true);
  room.apply(read(dm, 1));
  assert.equal(room.unseen("s"), false);
  room.apply(failed(2));
  room.apply(read(dm, 1));
  assert.equal(room.unseen("s"), true);
  room.apply(read(feed, 2));
  assert.equal(room.unseen("s"), false);
});

test("rereading gives the feed and each DM their newest read positions", async () => {
  const listed = { lastRead: 2, name: "Agent", parent: "", profile: "p", box: "b", boxState: "ready", computer: "stopped", goal: "", startedAt: "", hadJobs: false } as const;
  const sessions: SessionSummary[] = [{ id: "s", ...listed }];
  const server = engine(0, sessions);
  server.state.feedRead = 5;
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    assert.deepEqual([room.readAt(feed), room.readAt(dm)], [5, 2]);
    server.state.feedRead = 8;
    sessions[0] = { ...sessions[0], lastRead: 4 };
    await room.rereadPositions();
    assert.deepEqual([room.readAt(feed), room.readAt(dm)], [8, 4]);
  } finally { server.restore(); }
});

test("a read made before the first hello, or while the stream is down, reaches the store", async () => {
  const listed = { lastRead: 2, name: "Agent", parent: "", profile: "p", box: "b", boxState: "ready", computer: "stopped", goal: "", startedAt: "", hadJobs: false } as const;
  const sessions: SessionSummary[] = [{ id: "s", ...listed }];
  const server = engine(0, sessions);
  const hellos: ((event: MessageEvent<string>) => void)[] = [];
  const sources: EventConnection[] = [];
  let reconnect = () => {};
  const factory = (): EventConnection => {
    const source: EventConnection = { onmessage: null, onerror: null, addEventListener(_type, cb) { hellos.push(cb); }, close() {} };
    sources.push(source);
    return source;
  };
  const connect: typeof stream = (room, after, apply, _factory, _schedule, opened) =>
    stream(room, after, apply, factory, cb => { reconnect = cb; return () => {}; }, opened);
  try {
    const room = new RoomStore("r", connect);
    await room.open();
    assert.deepEqual([room.readAt(feed), room.readAt(dm)], [0, 2]);
    server.state.feedRead = 5;
    sessions[0] = { ...sessions[0], lastRead: 4 };
    hellos[0](new MessageEvent("hello", { data: '{"build":"b"}' }));
    await room.rereading;
    assert.deepEqual([room.readAt(feed), room.readAt(dm)], [5, 4]);
    server.state.feedRead = 9;
    sources[0].onerror?.(new Event("error"));
    reconnect();
    hellos[1](new MessageEvent("hello", { data: '{"build":"b"}' }));
    await room.rereading;
    assert.deepEqual([room.readAt(feed), room.readAt(dm)], [9, 4]);
  } finally { server.restore(); }
});

test("session.list and later events set and clear the running computers", async () => {
  const listed = { lastRead: 0, name: "Agent", parent: "", profile: "p", box: "b", boxState: "ready", goal: "", startedAt: "", hadJobs: false } as const;
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

test("a pending box's reason comes from session.list, follows a pending to pending change, waits for the owner on room-host, and free text gives none", async () => {
  const original = globalThis.fetch;
  const session = { id: "s", lastRead: 0, name: "Agent", parent: "", profile: "p", box: "b", boxState: "pending", boxReason: "cpu", computer: "stopped", goal: "", startedAt: "", hadJobs: false };
  const changed = (seq: number, from: BoxState, to: BoxState, reason?: string) => event(seq, { type: "box.changed", body: { box: "b", session: "s", from, to, reason } });
  globalThis.fetch = async input => {
    if (String(input).endsWith("session.list")) return new Response(JSON.stringify({ sessions: [session], seq: 2 }));
    return new Response(JSON.stringify({ rooms: [] }));
  };
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    assert.equal(room.pendingReasons.get("s"), "cpu");
    room.apply(changed(3, "pending", "pending", "room-limit"));
    assert.deepEqual([room.pendingReasons.get("s"), room.status("s")], ["room-limit", "idle"]);
    // Only the owner can end a wait for a host that takes the room, so it waits for the owner.
    room.apply(changed(4, "pending", "pending", "room-host"));
    assert.equal(room.status("s"), "waiting");
    room.apply(changed(5, "creating", "pending", "dial tcp: connection refused"));
    assert.equal(room.pendingReasons.has("s"), false);
  } finally { globalThis.fetch = original; }
});

test("a session's host comes from session.list, follows its box's placement and moves, and goes with a deleted box", async () => {
  const original = globalThis.fetch;
  const session = { id: "s", lastRead: 0, name: "Agent", parent: "", profile: "p", box: "b", boxState: "ready", host: "arch", computer: "stopped", goal: "", startedAt: "", hadJobs: false };
  const unplaced = { id: "u", lastRead: 0, name: "New", parent: "", profile: "p", box: "c", boxState: "pending", computer: "stopped", goal: "", startedAt: "", hadJobs: false };
  const changed = (seq: number, id: string, from: BoxState, to: BoxState, host?: string) => event(seq, { type: "box.changed", body: { box: "b", session: id, host, from, to } });
  globalThis.fetch = async input => {
    if (String(input).endsWith("session.list")) return new Response(JSON.stringify({ sessions: [session, unplaced], seq: 2 }));
    return new Response(JSON.stringify({ rooms: [] }));
  };
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    assert.deepEqual([room.hosts.get("s"), room.hosts.has("u")], ["arch", false]);
    room.apply(changed(3, "u", "pending", "creating", "lab"));
    assert.equal(room.hosts.get("u"), "lab");
    room.apply(changed(4, "s", "ready", "stopped", "arch"));
    assert.equal(room.hosts.get("s"), "arch");
    room.apply(changed(5, "s", "stopped", "pending", "gpu-box"));
    assert.equal(room.hosts.get("s"), "gpu-box");
    room.apply(changed(6, "u", "creating", "pending"));
    assert.equal(room.hosts.has("u"), false);
    // The event of a deleted box still carries the host the box left.
    room.apply(changed(7, "s", "closing", "deleted", "gpu-box"));
    assert.equal(room.hosts.has("s"), false);
  } finally { globalThis.fetch = original; }
});

test("a failed job counts after a fresh load until the owner reads past its end, and the page does not wait for it", async () => {
  const original = globalThis.fetch;
  let answer = (_: Response) => {};
  const ends = [
    event(1, { type: "shell.job.ended", body: { session: "s", job: "read", exitCode: 1, tail: "" } }),
    event(3, { type: "shell.job.ended", body: { session: "s", job: "passed", exitCode: 0, tail: "" } }),
    event(4, { type: "shell.job.ended", body: { session: "s", job: "unread", exitCode: 1, tail: "" } }),
  ];
  const session = { id: "s", lastRead: 2, name: "Agent", parent: "", profile: "p", box: "b", boxState: "ready", computer: "stopped", goal: "", startedAt: "", hadJobs: true };
  globalThis.fetch = async input => {
    if (String(input).endsWith("session.list")) return new Response(JSON.stringify({ sessions: [session], seq: 4 }));
    if (String(input).endsWith("events.list")) return new Promise<Response>(done => { answer = done; });
    return new Response(JSON.stringify({ rooms: [] }));
  };
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    assert.equal(room.ready, true);
    answer(new Response(JSON.stringify({ events: ends })));
    await new Promise(setImmediate);
    assert.deepEqual([...room.failedJobs], ["s/unread"]);
  } finally { globalThis.fetch = original; }
});

test("a read that passes a counted failed job clears its mark, even when the read arrives later", async () => {
  const original = globalThis.fetch;
  let answer = (_: Response) => {};
  const ends = [
    event(3, { type: "shell.job.ended", body: { session: "s", job: "passed", exitCode: 0, tail: "" } }),
    event(4, { type: "shell.job.ended", body: { session: "s", job: "unread", exitCode: 1, tail: "" } }),
  ];
  const session = { id: "s", lastRead: 2, name: "Agent", parent: "", profile: "p", box: "b", boxState: "ready", computer: "stopped", goal: "", startedAt: "", hadJobs: true };
  globalThis.fetch = async input => {
    if (String(input).endsWith("session.list")) return new Response(JSON.stringify({ sessions: [session], seq: 4 }));
    if (String(input).endsWith("room.list")) return new Response(JSON.stringify({ rooms: [], seq: 4 }));
    if (String(input).endsWith("events.list")) return new Promise<Response>(done => { answer = done; });
    return new Response(JSON.stringify({ rooms: [], sessions: [], operations: [], seq: 4 }));
  };
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    answer(new Response(JSON.stringify({ events: ends })));
    await new Promise(setImmediate);
    assert.deepEqual([...room.failedJobs], ["s/unread"]);
    room.apply(event(0, { type: "conversation.read", body: { room: "r", conversation: dm, seq: 4 } }));
    assert.equal(room.failedJobs.size, 0);
  } finally { globalThis.fetch = original; }
});

test("a job that ends counts as ended at once, even while a turn holds the running count", () => {
  const room = new RoomStore("r", () => () => {});
  room.apply(event(1, { type: "turn.started", body: { session: "s", turn: "t", inputs: [] } }));
  room.apply(event(2, { type: "shell.job.ended", body: { session: "s", job: "j", exitCode: 0, tail: "" } }));
  assert.deepEqual([...room.endedJobs], ["s/j"]);
  assert.equal(room.failedJobs.size, 0);
});

test("a session with a finished or running job stays reachable after a fresh load, and one that gains a job joins it", async () => {
  const listed = { lastRead: 0, name: "Agent", parent: "", profile: "p", box: "b", boxState: "ready", computer: "stopped", goal: "", startedAt: "" } as const;
  const server = engine(3, [{ id: "s", ...listed, hadJobs: true }, { id: "r", ...listed, hadJobs: false, shellJobs: 1 }, { id: "x", ...listed, hadJobs: false }]);
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    assert.deepEqual([...room.hadJobs], ["s", "r"]);
    room.apply(event(4, { type: "shell.job.ended", body: { session: "x", job: "j", exitCode: 0, tail: "" } }));
    assert.deepEqual([...room.hadJobs], ["s", "r", "x"]);
  } finally { server.restore(); }
});

test("activity labels follow the running turn and never reach a window", async () => {
  const listed = { lastRead: 0, name: "Agent", parent: "", profile: "p", box: "b", boxState: "ready", computer: "stopped", goal: "", startedAt: "", hadJobs: false } as const;
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
    room.apply(label("t", "ran go test"));
    assert.equal(room.activity.get("s"), "ran go test");
    assert.deepEqual(room.steps.get("s"), ["read spec.md", "ran go test"]);
    room.apply(event(2, { type: "turn.ended", body: { session: "s", turn: "t", outcome: "completed", usage } }));
    assert.equal(room.activity.has("s"), false);
    assert.equal(room.steps.has("s"), false);
    assert.equal(feedWindow.ordered.some(item => item.type === "turn.activity"), false);
  } finally { server.restore(); }
});

test("a session counts once: a broken box, then waiting, then a running turn, then a failed turn not yet seen", () => {
  const store = new RoomStore("r");
  store.apply(event(1, { type: "session.started", body: { session: "a", name: "A", profile: "p" } }));
  store.apply(event(2, { type: "session.started", body: { session: "b", name: "B", profile: "p" } }));
  store.apply(event(3, { type: "turn.started", body: { session: "a", turn: "t1", inputs: [] } }));
  store.apply(event(4, { type: "turn.started", body: { session: "b", turn: "t2", inputs: [] } }));
  store.apply(event(5, { type: "op.requested", body: { id: "o", kind: "hostexec", by: { kind: "session", session: "a" }, class: "owner",
    card: { title: "Run", target: { kind: "host", name: "h" } }, expires: "" } }));
  assert.deepEqual([store.status("a"), store.counts], ["waiting", { sessions: 2, waiting: 1, failed: 0, working: 1, idle: 0 }]);
  // Approving does not repair a box that was lost, so the failure shows while the request still waits.
  store.apply(event(6, { type: "box.changed", body: { box: "x", session: "a", to: "lost" } }));
  assert.deepEqual(store.counts, { sessions: 2, waiting: 0, failed: 1, working: 1, idle: 0 });
  store.apply(event(7, { type: "box.changed", body: { box: "x", session: "a", to: "ready" } }));
  store.apply(event(8, { type: "op.decided", body: { id: "o", state: "approved", decision: { by: { kind: "owner" }, approved: true, at: "" } } }));
  assert.deepEqual(store.counts, { sessions: 2, waiting: 0, failed: 0, working: 2, idle: 0 });
  // A failed turn shows until seen, but a new turn moves on from it.
  store.apply(event(9, { type: "turn.ended", body: { session: "b", turn: "t2", outcome: "failed", usage } }));
  assert.deepEqual(store.counts, { sessions: 2, waiting: 0, failed: 1, working: 1, idle: 0 });
  store.apply(event(10, { type: "turn.started", body: { session: "b", turn: "t3", inputs: [] } }));
  assert.deepEqual(store.counts, { sessions: 2, waiting: 0, failed: 0, working: 2, idle: 0 });
});

test("a session's goals load with the room and follow goal events", async () => {
  const listed = { lastRead: 0, name: "Agent", parent: "", profile: "p", box: "b", boxState: "ready", computer: "stopped", goal: "the start text", startedAt: "", hadJobs: false } as const;
  const server = engine(0, [{ id: "s", ...listed }]);
  const inner = globalThis.fetch;
  const goal = (text: string) => ({ id: "g1", room: "r", session: "s", text, doneWhen: "it ships", state: "active", items: [{ number: 1, text: "a step", state: "open" }], created: "", updated: "" });
  let goals = [goal("first")];
  globalThis.fetch = async (input, init) => String(input).endsWith("/goal.list") ? new Response(JSON.stringify({ goals })) : inner(input, init);
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    await new Promise(resolve => setTimeout(resolve));
    assert.equal(room.goals.get("s")?.[0].text, "first");
    goals = [goal("second")];
    room.apply(event(1, { type: "goal.set", body: { goal: "g1", session: "s", text: "second" } }));
    await new Promise(resolve => setTimeout(resolve));
    assert.equal(room.goals.get("s")?.[0].text, "second");
    goals = [];
    room.apply(event(2, { type: "goal.ended", body: { goal: "g1", session: "s", state: "done", summary: "shipped" } }));
    await new Promise(resolve => setTimeout(resolve));
    assert.equal(room.goals.has("s"), false);
  } finally { globalThis.fetch = inner; server.restore(); }
});

test("a lost box's open turn is not running, and comes back with the box", async () => {
  const listed = { lastRead: 0, name: "Agent", parent: "", profile: "p", box: "b", boxState: "ready", computer: "stopped", goal: "", startedAt: "", hadJobs: false } as const;
  const server = engine(0, [{ id: "s", ...listed }]);
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    room.apply(event(1, { type: "turn.started", body: { session: "s", turn: "t", inputs: [] } }));
    assert.equal(room.running("s"), true);
    room.apply(event(2, { type: "box.changed", body: { box: "b", session: "s", from: "ready", to: "lost", reason: "box disconnected" } }));
    assert.equal(room.running("s"), false);
    assert.equal(room.status("s"), "failed");
    room.apply(event(3, { type: "box.changed", body: { box: "b", session: "s", from: "lost", to: "ready" } }));
    assert.equal(room.running("s"), true);
  } finally { server.restore(); }
});

test("a box that returns reads back the open turn session.list left out", async () => {
  const listed = { lastRead: 0, name: "Agent", parent: "", profile: "p", box: "b", computer: "stopped", goal: "", startedAt: "", hadJobs: false } as const;
  const sessions: SessionSummary[] = [{ id: "s", ...listed, boxState: "lost" }];
  const server = engine(0, sessions);
  try {
    const room = new RoomStore("r", () => () => {});
    await room.open();
    assert.equal(room.running("s"), false);
    sessions[0] = { id: "s", ...listed, boxState: "ready", turn: "t" };
    server.posts.push(event(1, { type: "post.created", body: { text: "later", home: feed } })); // session.list now reads at seq 1.
    room.apply(event(1, { type: "box.changed", body: { box: "b", session: "s", from: "lost", to: "ready" } }));
    await new Promise(resolve => setTimeout(resolve));
    assert.equal(room.running("s"), true);
  } finally { server.restore(); }
});
