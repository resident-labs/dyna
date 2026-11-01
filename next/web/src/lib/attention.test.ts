import { test } from "node:test";
import assert from "node:assert/strict";
import { Attention } from "./attention.svelte.ts";
import { boxReason } from "./feed.ts";
import type { Event, EventBody, Scope } from "./contract.gen.ts";

const event = (seq: number, room: string, body: EventBody): Event => ({ seq, id: String(seq), at: "", room, from: { kind: "owner" }, ...body });
const card = { title: "Push", target: { kind: "room", name: "a" } } as const;
const decision = { by: { kind: "owner" }, approved: true, at: "" } as const;

test("a session whose box failed with its turn open is broken, not working, until the box is ready again", async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async input => new Response(JSON.stringify(String(input).endsWith("device.scopes")
    ? { scopes: ["read"] }
    : { seq: 1, rooms: [{ id: "a", name: "A", created: "", waiting: [], working: ["s1", "s2"] }] }));
  try {
    const attention = new Attention(() => () => {});
    await attention.open();
    const box = (seq: number, to: "failed" | "ready") => attention.apply(event(seq, "a", { type: "box.changed", body: { box: "b", session: "s1", to } }));
    box(2, "failed");
    assert.deepEqual([attention.busy("a"), [...attention.broken.get("a")!], attention.failing("a")], [["s2"], [["s1", { box: "failed", cause: undefined, at: "" }]], 1]);
    box(3, "ready");
    assert.deepEqual([attention.busy("a"), attention.broken.get("a")!.size, attention.failing("a")], [["s1", "s2"], 0, 0]);
    attention.seed([{ id: "s2", boxState: "lost" }], { id: "a", seq: 3 });
    assert.deepEqual(attention.busy("a"), ["s1"]);
    assert.equal(boxReason({ box: "failed", cause: "host_lost" }), "the host stopped answering during the run");
  } finally { globalThis.fetch = original; }
});

test("a room's hostless count loads which sessions wait for a host that takes the room, after a failed read, until the box leaves pending", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const original = globalThis.fetch;
  let offline = true;
  const unexpected: string[] = [];
  globalThis.fetch = async input => {
    const method = String(input).replace("/v1/", "");
    if (method === "room.list") return new Response(JSON.stringify({ seq: 3, rooms: [{ id: "a", name: "A", created: "", waiting: [], working: ["s1"], failed: 0, hostless: 1 }] }));
    if (method === "device.scopes") return new Response(JSON.stringify({ scopes: ["read"] }));
    if (method !== "session.list") { unexpected.push(method); throw new Error(`Unexpected fetch: ${method}`); }
    if (offline) throw new Error("offline");
    return new Response(JSON.stringify({ seq: 3, sessions: [{ id: "s1", boxState: "pending", boxReason: "room-host" }, { id: "s2", boxState: "pending", boxReason: "cpu" }] }));
  };
  try {
    const attention = new Attention(() => () => {});
    await attention.open();
    await new Promise(setImmediate);
    offline = false;
    t.mock.timers.tick(1000);
    await new Promise(setImmediate);
    assert.deepEqual([[...attention.hostless.get("a")!], attention.busy("a")], [["s1"], []]);
    attention.apply(event(4, "a", { type: "box.changed", body: { box: "b", session: "s1", from: "pending", to: "creating" } }));
    assert.equal(attention.hostless.get("a")!.size, 0);
    assert.deepEqual(unexpected, []);
  } finally { globalThis.fetch = original; }
});

test("Home's session read and the room's own seed share one read, so a box change during it applies on top", async () => {
  const original = globalThis.fetch;
  let reads = 0, answer!: () => void;
  const unexpected: string[] = [];
  globalThis.fetch = async input => {
    if (String(input).endsWith("room.list")) return new Response(JSON.stringify({ seq: 3, rooms: [{ id: "a", name: "A", created: "", waiting: [], working: [], failed: 0, hostless: 1 }] }));
    if (String(input).endsWith("device.scopes")) return new Response(JSON.stringify({ scopes: ["read"] }));
    if (!String(input).endsWith("session.list")) { unexpected.push(String(input)); throw new Error(`Unexpected fetch: ${String(input)}`); }
    reads++;
    await new Promise<void>(done => { answer = done; });
    return new Response(JSON.stringify({ seq: 3, sessions: [{ id: "s1", boxState: "pending", boxReason: "room-host" }] }));
  };
  try {
    const attention = new Attention(() => () => {});
    await attention.open();
    const home = attention.listSessions("a");
    attention.apply(event(4, "a", { type: "box.changed", body: { box: "b", session: "s1", from: "pending", to: "creating" } }));
    await new Promise(setImmediate);
    answer();
    await home;
    assert.deepEqual([reads, attention.hostless.get("a")!.size], [1, 0]);
    assert.deepEqual(unexpected, []);
  } finally { globalThis.fetch = original; }
});

test("room.list seeds each room's waiting requests and working sessions, and the owner stream updates them", async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => new Response(JSON.stringify({ seq: 5, rooms: [
    { id: "a", name: "A", created: "", waiting: ["o1"], working: ["s1"], latest: { session: "s1", name: "lead", label: "booting" } }, { id: "b", name: "B", created: "", waiting: null, working: null }] }));
  const urls: string[] = [];
  try {
    const attention = new Attention((room, after, apply) => { urls.push(`${room}/${after()}`); return () => {}; });
    await attention.open();
    assert.deepEqual(urls, ["undefined/5"]);
    assert.equal(attention.activity.get("s1"), "booting");
    // Events at or before the list's seq are already in it.
    attention.apply(event(5, "a", { type: "turn.ended", body: { session: "s1", turn: "t", outcome: "completed", usage: { model: "m", inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 } } }));
    attention.apply(event(6, "b", { type: "op.requested", body: { id: "o2", kind: "k", by: { kind: "owner" }, class: "owner", card, expires: "" } }));
    attention.apply(event(8, "a", { type: "op.decided", body: { id: "o1", state: "approved", decision } }));
    attention.apply(event(9, "b", { type: "turn.started", body: { session: "s2", turn: "t", inputs: [] } }));
    assert.deepEqual([[...attention.waiting.get("a")!], [...attention.waiting.get("b")!]], [[], ["o2"]]);
    assert.deepEqual([[...attention.working.get("a")!], [...attention.working.get("b")!]], [["s1"], ["s2"]]);
    // Activity has seq 0 and counts for a working session's current turn only.
    const label = (session: string, turn: string, text: string) => attention.apply(event(0, session === "s1" ? "a" : "b", { type: "turn.activity", body: { session, turn, label: text } }));
    label("s1", "t0", "read spec.md");
    label("s2", "t", "edited App.svelte");
    label("s2", "old", "stale");
    label("s3", "t", "not working");
    assert.deepEqual([...attention.activity], [["s1", "read spec.md"], ["s2", "edited App.svelte"]]);
    // session.list seeds a working session's turn and label once; a live label and turn stay.
    const summary = (id: string, turn: string, activity: string) => ({ id, turn, activity, boxState: "ready" as const });
    attention.seed([summary("s1", "t1", "older"), summary("s2", "t", "older"), summary("s3", "t", "not working")]);
    assert.deepEqual([...attention.activity], [["s1", "read spec.md"], ["s2", "edited App.svelte"]]);
    label("s1", "t0", "stale turn");
    label("s1", "t1", "ran node --test");
    // The newest label is last, so it names the room's busiest agent.
    assert.deepEqual([...attention.activity], [["s2", "edited App.svelte"], ["s1", "ran node --test"]]);
    // A session closed during its turn sends no turn.ended.
    attention.apply(event(10, "a", { type: "session.closed", body: { session: "s1" } }));
    assert.equal(attention.working.get("a")!.size, 0);
    assert.deepEqual([...attention.activity.keys()], ["s2"]);
    // A room's session.list seeds its sessions. Events that arrive before the answer wait, and those after its seq
    // apply on top; later ones apply at once.
    globalThis.fetch = async () => new Response(JSON.stringify({ seq: 11, sessions: [{ id: "s2" }, { id: "s4" }] }));
    const listed = attention.listSessions("b");
    const started = (seq: number, session: string) => event(seq, "b", { type: "session.started", body: { session, name: "n", profile: "p" } });
    attention.apply(started(11, "s4"));
    attention.apply(started(12, "s5"));
    await listed;
    attention.apply(event(13, "b", { type: "session.closed", body: { session: "s4" } }));
    assert.deepEqual([...attention.sessions.get("b")!], ["s2", "s5"]);
  } finally { globalThis.fetch = original; }
});

test("a room created after the page loaded shows, with the events that came while room.list ran, after a failed read", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const original = globalThis.fetch;
  let fail!: () => void;
  globalThis.fetch = async () => new Response(JSON.stringify({ seq: 5, rooms: [] }));
  try {
    const attention = new Attention(() => () => {});
    await attention.open();
    globalThis.fetch = () => new Promise<Response>((_, reject) => { fail = () => reject(new Error("offline")); });
    attention.apply(event(6, "n", { type: "session.started", body: { session: "root", name: "root", profile: "p" } }));
    attention.apply(event(8, "n", { type: "turn.started", body: { session: "root", turn: "t", inputs: [] } }));
    fail();
    await new Promise(done => setImmediate(done));
    globalThis.fetch = async () => new Response(JSON.stringify({ seq: 7, rooms: [{ id: "n", name: "N", created: "", lastRead: 0, waiting: null, working: null }] }));
    t.mock.timers.tick(1000);
    await new Promise(done => setImmediate(done));
    assert.deepEqual([[...attention.rooms.keys()], [...attention.working.get("n") ?? []]], [["n"], ["root"]]);
  } finally {
    globalThis.fetch = original;
  }
});

test("a room counts a session as failed until the owner reads past its failure, and a completed turn clears it", async () => {
  const fetched = globalThis.fetch;
  globalThis.fetch = async input => new Response(JSON.stringify(String(input).endsWith("room.list")
    ? { seq: 5, rooms: [{ id: "a", name: "A", created: "", lastRead: 0, waiting: null, working: null, failed: 1 }] }
    : String(input).endsWith("session.list")
      ? { seq: 5, sessions: [{ id: "s1", lastRead: 0, boxState: "ready", lastOutcome: "failed" }, { id: "s2", lastRead: 4, boxState: "ready", lastOutcome: "failed" }] }
      : { events: [turn(3, "s1", "failed"), turn(4, "s2", "failed")] }));
  const turn = (seq: number, session: string, outcome: "failed" | "completed") =>
    event(seq, "a", { type: "turn.ended", body: { session, turn: "t", outcome, usage: { model: "m", inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 } } });
  try {
    const attention = new Attention(() => () => {});
    await attention.open();
    await new Promise(resolve => setImmediate(resolve));
    await attention.listSessions("a");
    assert.equal(attention.failing("a"), 1); // s2's DM has read past its failure
    attention.apply(turn(6, "s2", "failed"));
    assert.equal(attention.failing("a"), 2);
    attention.apply(turn(7, "s2", "completed"));
    attention.apply(event(0, "a", { type: "conversation.read", body: { room: "a", conversation: { kind: "dm", session: "s1" }, seq: 3 } }));
    assert.equal(attention.failing("a"), 0);
  } finally { globalThis.fetch = fetched; }
});

test("the owner stream keeps a room's paused state current", async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => new Response(JSON.stringify({ seq: 1, rooms: [{ id: "a", name: "A", paused: false, created: "", lastRead: 0, waiting: [], working: [] }] }));
  try {
    const attention = new Attention(() => () => {});
    await attention.open();
    attention.apply(event(2, "a", { type: "room.paused", body: {} }));
    assert.equal(attention.rooms.get("a")?.paused, true);
    attention.apply(event(3, "a", { type: "room.resumed", body: {} }));
    assert.equal(attention.rooms.get("a")?.paused, false);
  } finally { globalThis.fetch = original; }
});

test("the owner stream marks a room deleting, and removes it when it is gone", async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => new Response(JSON.stringify({ seq: 1, rooms: [{ id: "a", name: "A", paused: false, created: "", lastRead: 0, waiting: [], working: [] }] }));
  try {
    const attention = new Attention(() => () => {});
    await attention.open();
    attention.apply(event(2, "a", { type: "room.deleting", body: {} }));
    assert.equal(attention.rooms.get("a")?.deleting, true);
    attention.apply(event(0, "a", { type: "room.deleted", body: {} }));
    assert.equal(attention.rooms.has("a"), false);
    assert.equal(attention.waiting.has("a"), false);
  } finally { globalThis.fetch = original; }
});

test("the page reads its device's scopes once at connect, and offers every control when the engine has no such method", async () => {
  const original = globalThis.fetch;
  const connect = async (scopes: Response) => {
    globalThis.fetch = async input => String(input).endsWith("/device.scopes") ? scopes : new Response(JSON.stringify({ seq: 1, rooms: [] }));
    const attention = new Attention(() => () => {});
    await attention.open();
    return attention;
  };
  try {
    const reader = await connect(new Response(JSON.stringify({ scopes: ["read"] })));
    assert.deepEqual(["read", "operate", "terminal", "admin"].map(scope => reader.can(scope as Scope)), [true, false, false, false]);
    const old = await connect(new Response(JSON.stringify({ code: "not_found", message: "not found" }), { status: 404 }));
    assert.equal(old.can("operate"), true);
  } finally { globalThis.fetch = original; }
});
