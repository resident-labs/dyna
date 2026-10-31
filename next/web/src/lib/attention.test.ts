import { test } from "node:test";
import assert from "node:assert/strict";
import { Attention } from "./attention.svelte.ts";
import type { Event, EventBody } from "./contract.gen.ts";

const event = (seq: number, room: string, body: EventBody): Event => ({ seq, id: String(seq), at: "", room, from: { kind: "owner" }, ...body });
const card = { title: "Push", target: { kind: "room", name: "a" } } as const;
const decision = { by: { kind: "owner" }, approved: true, at: "" } as const;

test("room.list seeds each room's waiting requests and working sessions, and the owner stream updates them", async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => new Response(JSON.stringify({ seq: 5, rooms: [
    { id: "a", name: "A", created: "", waiting: ["o1"], working: ["s1"] }, { id: "b", name: "B", created: "", waiting: null, working: null }] }));
  const urls: string[] = [];
  try {
    const attention = new Attention((room, after, apply) => { urls.push(`${room}/${after()}`); return () => {}; });
    await attention.open();
    assert.deepEqual(urls, ["undefined/5"]);
    // Events at or before the list's seq are already in it.
    attention.apply(event(5, "a", { type: "turn.ended", body: { session: "s1", turn: "t", outcome: "completed", usage: { model: "m", inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 } } }));
    attention.apply(event(6, "b", { type: "op.requested", body: { id: "o2", kind: "k", by: { kind: "owner" }, class: "owner", card, expires: "" } }));
    attention.apply(event(7, "b", { type: "op.requested", body: { id: "o3", kind: "k", by: { kind: "owner" }, class: "agent", card, expires: "" } }));
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
    const summary = (id: string, turn: string, activity: string) => ({ id, turn, activity });
    attention.seed([summary("s1", "t1", "older"), summary("s2", "t", "older"), summary("s3", "t", "not working")]);
    assert.deepEqual([...attention.activity], [["s1", "read spec.md"], ["s2", "edited App.svelte"]]);
    label("s1", "t0", "stale turn");
    label("s1", "t1", "ran node --test");
    assert.equal(attention.activity.get("s1"), "ran node --test");
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
