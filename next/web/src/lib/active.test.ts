import { test } from "node:test";
import assert from "node:assert/strict";
import { activeRooms, doing, failedRooms } from "./active.ts";
import { Attention } from "./attention.svelte.ts";
import type { Event, EventBody, RoomSummary } from "./contract.gen.ts";

const event = (seq: number, room: string, body: EventBody): Event => ({ seq, id: String(seq), at: "", room, from: { kind: "owner" }, ...body });
const room = (id: string, working: string[]): RoomSummary => ({ id, name: id, paused: false, created: "", lastRead: 0, waiting: [], working, failed: 0, hostless: 0 });
async function opened(rooms: RoomSummary[]) {
  const original = globalThis.fetch;
  globalThis.fetch = async () => new Response(JSON.stringify({ seq: 1, rooms }));
  const attention = new Attention(() => () => {});
  await attention.open().finally(() => { globalThis.fetch = original; });
  return attention;
}
const failTurn = (seq: number, id: string, session: string) => event(seq, id, { type: "turn.ended", body: { session, turn: "t", outcome: "failed", cause: "model_failed", usage: { model: "m", inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 } } });

test("a label says what the session is doing in the present tense, and only a command is mono", () => {
  assert.deepEqual(doing("read spec.md"), { words: "reading spec.md" });
  assert.deepEqual(doing("edited App.svelte"), { words: "editing App.svelte" });
  assert.deepEqual(doing("reading the CI log"), { words: "reading the CI log" });
  assert.deepEqual(doing("bash npm run build"), { words: "running", command: "npm run build" });
  assert.deepEqual(doing("running go test ./engine/..."), { words: "running", command: "go test ./engine/..." });
});

test("failed rooms sort first, the others keep their order by name, and quiet rooms stay apart", async () => {
  const attention = await opened([room("zeta", ["z1", "z2"]), room("beta", ["b1"]), room("alpha", []), room("gamma", ["g1"])]);
  attention.apply(event(2, "gamma", { type: "turn.activity", body: { session: "g1", turn: "t", label: "edit main.go" } }));
  attention.apply(failTurn(3, "zeta", "z3"));
  const { active, quiet } = activeRooms(attention);
  assert.deepEqual(active.map(line => [line.room.id, line.failed, line.words]), [["zeta", 1, "2 working"], ["beta", 0, "1 working"], ["gamma", 0, "editing main.go"]]);
  assert.deepEqual(quiet.map(line => line.room.id), ["alpha"]);
});

test("a failed room is one Needs you row, and seeing the room clears it with its rail mark and column row", async () => {
  const attention = await opened([room("a", [])]);
  attention.apply(failTurn(2, "a", "s1"));
  attention.apply(failTurn(3, "a", "s2"));
  assert.deepEqual(failedRooms(attention, () => undefined).map(row => [row.title, row.reason]), [["2 sessions failed", "The model provider refused or failed the request"]]);
  assert.equal(activeRooms(attention).active.length, 1);
  attention.apply({ seq: 0, id: "", at: "", from: { kind: "owner" }, type: "conversation.read", body: { room: "a", conversation: { kind: "feed" }, seq: 3 } });
  assert.deepEqual([attention.failing("a"), failedRooms(attention, () => undefined).length, activeRooms(attention).active.length], [0, 0, 0]);
});
