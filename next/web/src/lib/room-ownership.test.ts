import { test } from "node:test";
import assert from "node:assert/strict";
import { RoomStore } from "./room.svelte.ts";

for (const early of [true, false]) test(`shell feed position arrives ${early ? "before" : "after"} room load`, async () => {
  const original = globalThis.fetch;
  const calls: string[] = [];
  globalThis.fetch = (async input => {
    const method = String(input).split("/").at(-1)!;
    calls.push(method);
    return new Response(JSON.stringify(method === "session.list" ? {
      seq: 10, sessions: [{ id: "s", name: "Agent", profile: "p", boxState: "ready", lastRead: 0, lastOutcome: "failed", startedAt: "" }],
    } : {}));
  }) as typeof fetch;
  const first = new RoomStore("r", () => () => {});
  const second = new RoomStore("other", () => () => {});
  const feed = { kind: "feed" } as const;
  try {
    if (early) first.noteRead(feed, 10);
    await first.open();
    if (!early) {
      assert.equal(first.unseen("s"), true);
      first.noteRead(feed, 10);
    }
    assert.equal(first.unseen("s"), false);
    first.noteRead(feed, 2); // A late older shell snapshot cannot regress the marker.
    assert.equal(first.readAt(feed), 10);
    assert.equal(calls.filter(method => method === "room.list").length, 0);
    await second.open();
    assert.equal(second.readAt(feed), 0, "a room switch does not inherit another room's position");
    assert.equal(second.unseen("s"), true);
    second.noteRead(feed, 10);
    assert.equal(second.unseen("s"), false);
  } finally { first.close(); second.close(); globalThis.fetch = original; }
});
