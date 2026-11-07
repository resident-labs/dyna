import { test } from "node:test";
import assert from "node:assert/strict";
import { RoomStore } from "./room.svelte.ts";
import { stream, type EventConnection } from "./stream.ts";

for (const failList of [false, true]) test(`snapshot-to-subscription gap, initial workflow list failure=${failList}`, async () => {
  const original = globalThis.fetch;
  let feedRead = 0, dmRead = 2, listCalls = 0, serverReadAtSubscription = -1;
  const sources: EventConnection[] = [];
  const hellos: ((event: MessageEvent<string>) => void)[] = [];
  let reconnect = () => {};
  const session = () => ({ id: "s", name: "Agent", profile: "p", boxState: "ready", lastRead: dmRead, startedAt: "" });
  const run = { run: "wf", room: "r", session: "crew", name: "Run", state: "done", profiles: [], steps: 0, failed: 0, started: "" };
  globalThis.fetch = (async input => {
    const method = String(input).split("/").at(-1);
    let body: unknown = {};
    if (method === "session.list") body = { sessions: [session()], seq: 10 };
    if (method === "room.list") body = { rooms: [{ id: "r", lastRead: feedRead }] };
    if (method === "workflow.list") {
      listCalls++;
      if (listCalls === 1) {
        // Both positions were snapshotted, but EventSource is not yet subscribed.
        // ConversationRead publishes only live: durable replay cannot repair this.
        feedRead = 5; dmRead = 4;
        if (failList) return new Response("temporary failure", { status: 503 });
      }
      body = { runs: [run] };
    }
    return new Response(JSON.stringify(body));
  }) as typeof fetch;
  const connect: typeof stream = (room, after, apply, _factory, _schedule, opened) => stream(room, after, apply, () => {
    serverReadAtSubscription = feedRead;
    const source: EventConnection = { onmessage: null, onerror: null, addEventListener(_, cb) { hellos.push(cb); }, close() {} };
    sources.push(source); return source;
  }, cb => { reconnect = cb; return () => {}; }, opened);
  const room = new RoomStore("r", connect);
  try {
    await room.open();
    assert.equal(room.ready, true);
    assert.equal(serverReadAtSubscription, 5);
    hellos[0](new MessageEvent("hello", { data: '{"build":"b"}' }));
    await room.rereading; await room.waitingRead;
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.equal(room.workflows.size, 1, "failed initial list recovers without requiring a reconnect");
    assert.equal(listCalls, failList ? 2 : 1, "healthy first hello does not repeat workflow.list");
    assert.equal(room.readAt({ kind: "feed" }), 5);
    assert.equal(room.readAt({ kind: "dm", session: "s" }), 4);
    sources[0].onerror?.(new Event("error")); reconnect();
    hellos[1](new MessageEvent("hello", { data: '{"build":"b"}' }));
    await room.rereading; await room.waitingRead;
    assert.equal(room.readAt({ kind: "feed" }), 5);
    assert.equal(room.readAt({ kind: "dm", session: "s" }), 4);
    assert.equal(room.workflows.size, 1);
    room.close(); const calls = listCalls;
    hellos[1](new MessageEvent("hello", { data: '{"build":"b"}' }));
    await Promise.resolve(); assert.equal(listCalls, calls);
  } finally { room.close(); globalThis.fetch = original; }
});

test("closing during a failed owning workflow read suppresses its recovery read", async () => {
  const original = globalThis.fetch;
  let listCalls = 0;
  let reply!: (response: Response) => void;
  let reading!: () => void;
  const started = new Promise<void>(resolve => { reading = resolve; });
  globalThis.fetch = (async input => {
    const method = String(input).split("/").at(-1);
    if (method === "workflow.list") {
      listCalls++; reading();
      return new Promise<Response>(resolve => { reply = resolve; });
    }
    return new Response(JSON.stringify(method === "session.list" ? { sessions: [], seq: 0 } : {}));
  }) as typeof fetch;
  const room = new RoomStore("r", () => { assert.fail("closed room must not subscribe"); });
  try {
    const opening = room.open(); await started;
    room.close(); reply(new Response("unavailable", { status: 503 })); await opening;
    assert.equal(listCalls, 1); assert.equal(room.workflows.size, 0);
  } finally { room.close(); globalThis.fetch = original; }
});
