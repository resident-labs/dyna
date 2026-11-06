import test from "node:test";
import assert from "node:assert/strict";
import { feedRows } from "./feed.ts";
import { loadFeedDepth, saveFeedDepth, sessionDepths, showsPost, type FeedDepth } from "./feed-depth.ts";
import type { Event, Principal } from "./contract.gen.ts";

const session = (session: string): Principal => ({ kind: "session", session });
const owner: Principal = { kind: "owner" };
const depths = sessionDepths([
  { session: "deep", parent: "worker" }, { session: "root" },
  { session: "lead", parent: "root" }, { session: "lead-b", parent: "root" },
  { session: "worker", parent: "lead" }, { session: "deeper", parent: "deep" },
  { session: "orphan", parent: "closed" },
]);
const post = (seq: number, from: Principal, mentions: Principal[]): Event => ({
  id: `event-${seq}`, seq, at: "2026-11-06T10:00:00Z", room: "room-a", from,
  type: "post.created", body: { home: { kind: "feed" }, text: `post-${seq}`, mentions },
});
const posts = (events: Event[], mode: FeedDepth) => feedRows(events, { kind: "feed" }, [], 0, new Set(), { mode, depths }).filter(row => row.kind === "post");

test("Root hides lead-to-lead despite their shallow depth; Leads includes it", () => {
  const events = [post(1, session("lead"), [session("lead-b")])];
  assert.equal(posts(events, "root").length, 0);
  assert.equal(posts(events, "leads").length, 1);
});

test("depth follows the complete known tree, independent of session list order", () => {
  assert.deepEqual([...depths].sort(), [["deep", 3], ["deeper", 4], ["lead", 1], ["lead-b", 1], ["root", 0], ["worker", 2]]);
  assert.equal(depths.has("orphan"), false, "a missing parent is not a root");
  assert.deepEqual([...sessionDepths([{ session: "root" }, { session: "worker", parent: "root" }])], [["root", 0], ["worker", 1]], "a moved session uses its current parent");
});

test("Root and owner are exact from/to predicates at arbitrary depth in both filtered modes", () => {
  for (const mode of ["root", "leads"] as const) {
    for (const participant of [owner, session("root")]) {
      assert.equal(posts([post(1, participant, [])], mode).length, 1);
      assert.equal(posts([post(1, participant, [session("deeper")])], mode).length, 1);
      assert.equal(posts([post(1, session("deeper"), [participant])], mode).length, 1);
      assert.equal(posts([post(1, session("deep"), [session("lead"), participant, session("deeper")])], mode).length, 1, "every recipient is checked");
    }
  }
});

test("Leads adds known MAX(sender, ALL recipients) depth at most 2, and All never filters", () => {
  const cases: [string, string[], boolean][] = [
    ["lead", [], true], ["lead", ["lead-b"], true], ["worker", ["lead"], true],
    ["lead", ["worker"], true], ["worker", ["worker", "lead-b"], true],
    ["deep", ["lead"], false], ["lead", ["deep"], false], ["deeper", [], false],
    ["lead", ["lead-b", "deep", "worker"], false], ["deep", ["deeper", "lead"], false],
  ];
  for (const [sender, recipients, allowed] of cases) {
    const events = [post(1, session(sender), recipients.map(session))];
    assert.equal(posts(events, "root").length, 0);
    assert.equal(posts(events, "leads").length, Number(allowed), JSON.stringify({ sender, recipients }));
    assert.equal(posts(events, "all").length, 1);
  }
});

test("unknown ancestry fails open in Leads, even with a known deep participant, but not Root", () => {
  for (const unknown of [session("closed"), session("foreign-root"), session("orphan"), { kind: "engine" } as Principal]) {
    for (const [from, to] of [[unknown, []], [unknown, [session("deep")]], [session("deep"), [unknown]], [session("lead"), [unknown, session("deep")]], [session("lead"), [session("deep"), unknown]]] as [Principal, Principal[]][]) {
      assert.equal(showsPost(from, to, { mode: "leads", depths }), true);
      assert.equal(showsPost(from, to, { mode: "root", depths }), false);
      assert.equal(showsPost(from, to, { mode: "all", depths }), true);
    }
    assert.equal(showsPost(owner, [unknown], { mode: "root", depths }), true);
    assert.equal(showsPost(unknown, [session("root")], { mode: "root", depths }), true);
  }
});

test("activity stays visible, hidden posts leave no dividers and name lines remain correct", () => {
  const activity: Event = { seq: 2, id: "work", at: "2026-11-06T10:01:00Z", from: { kind: "engine" }, type: "session.started", body: { session: "deeper", parent: "deep", name: "builder", profile: "sol" } };
  const hidden = post(1, session("deep"), [session("lead")]);
  assert.deepEqual(feedRows([hidden], { kind: "feed" }, [], 1, new Set(), { mode: "leads", depths }), []);
  for (const mode of ["root", "leads", "all"] as const) {
    const rows = feedRows([hidden, activity], { kind: "feed" }, [], 0, new Set(), { mode, depths });
    assert.equal(rows.filter(row => row.kind === "work").length, 1);
  }
  const visible = posts([post(1, session("lead"), [owner]), post(2, session("worker"), []), post(3, session("lead-b"), [owner]), post(4, session("lead-b"), [owner])], "root");
  assert.deepEqual(visible.map(row => [row.key, row.first]), [["p1", true], ["p3", true], ["p4", false]]);
});

test("final targets, carried replies and pending recipients follow the predicate; DMs do not filter", () => {
  const usage = { model: "m", inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 };
  const ended: Event = { seq: 2, id: "ended", at: "2026-11-06T10:01:00Z", from: { kind: "engine" }, type: "turn.ended", body: { session: "deep", turn: "t", outcome: "completed", usage, final: "done", replies: [{ reply: { kind: "final", target: session("lead"), home: { kind: "feed" } } }] } };
  assert.equal(posts([ended], "root").length, 0);
  assert.equal(posts([ended], "leads").length, 0);
  assert.equal(posts([ended], "all").length, 1);
  const multiple: Event = { ...ended, body: { ...ended.body, replies: [...ended.body.replies!, { reply: { kind: "final", target: owner, home: { kind: "feed" } } }] } };
  assert.equal(posts([multiple], "root").length, 1, "a second final reply target still participates");
  const deepTarget: Event = { ...ended, body: { ...ended.body, session: "lead", replies: [...ended.body.replies!, { reply: { kind: "final", target: session("deep"), home: { kind: "feed" } } }] } };
  assert.equal(posts([deepTarget], "leads").length, 0, "a second final target participates in maximum depth too");
  const carried: Event = { ...ended, body: { ...ended.body, replies: [{ reply: { kind: "final", target: owner, home: { kind: "feed" } }, posted: "event-1" }] } };
  assert.equal(posts([post(1, session("deep"), []), carried], "root").length, 1, "the carried final target is a recipient too");
  const pending = [{ id: 1, at: ended.at, text: "pending", attachments: [], to: [session("deep")], failed: true }];
  assert.equal(feedRows([], { kind: "feed" }, pending, 0, new Set(), { mode: "root", depths }).filter(row => row.kind === "post").length, 1, "pending owner post is always included");
  const dm: Event = { ...post(1, session("deep"), []), type: "post.created", body: { home: { kind: "dm", session: "deep" }, text: "private" } };
  assert.equal(feedRows([dm], { kind: "dm", session: "deep" }, [], 0, new Set(), { mode: "root", depths }).filter(row => row.kind === "post").length, 1);
});

test("localStorage restores each room, defaults to All and tolerates invalid/blocked storage", () => {
  const values = new Map<string, string>();
  const storage = () => ({ getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => { values.set(key, value); } });
  assert.equal(loadFeedDepth("a", storage), "all");
  saveFeedDepth("a", "root", storage); saveFeedDepth("b", "leads", storage);
  assert.equal(loadFeedDepth("a", storage), "root"); assert.equal(loadFeedDepth("b", storage), "leads");
  assert.equal(loadFeedDepth("c", storage), "all");
  saveFeedDepth("a", "all", storage); assert.equal(loadFeedDepth("a", storage), "all");
  assert.equal(loadFeedDepth("b", storage), "leads");
  for (const invalid of ["", "Root", "1", "2", "NaN", "{}", "unknown"]) {
    values.set("dyna.feed-depth:a", invalid); assert.equal(loadFeedDepth("a", storage), "all", invalid);
  }
  const blocked = () => { throw new Error("storage blocked"); };
  assert.equal(loadFeedDepth("a", blocked), "all");
  assert.doesNotThrow(() => saveFeedDepth("a", "root", blocked));
});
