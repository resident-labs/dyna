import { test } from "node:test";
import assert from "node:assert/strict";
import { APIError, blockedBy, call } from "./client.ts";

test("turns a non-JSON error answer into a typed error", async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => new Response("<html>Bad gateway</html>", { status: 502 });
  try {
    await assert.rejects(call("room.list", {}), (error: unknown) =>
      error instanceof APIError && error.detail.code === "unavailable");
  } finally { globalThis.fetch = original; }
});

test("a plain-text 403 from the web server means signed out", async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => new Response("forbidden\n", { status: 403 });
  try {
    await assert.rejects(call("room.list", {}), (error: unknown) => blockedBy(error) === "signed-out");
  } finally { globalThis.fetch = original; }
});

test("identical reads in flight at once share one request, each with its own copy; writes never share", async () => {
  const original = globalThis.fetch;
  let requests = 0;
  globalThis.fetch = async () => { requests++; await new Promise(resolve => setTimeout(resolve, 10)); return new Response(JSON.stringify({ items: [{ id: "a" }] }), { status: 200 }); };
  try {
    const [a, b] = await Promise.all([call("session.list", { room: "r" }), call("session.list", { room: "r" })]);
    assert.equal(requests, 1);
    assert.notEqual(a, b);
    assert.deepEqual(a, b);
    await call("session.list", { room: "r" });
    assert.equal(requests, 2);
    await Promise.all([call("session.list", { room: "r" }), call("session.list", { room: "other" })]);
    assert.equal(requests, 4);
    await Promise.all([call("session.stop", { session: "s" }), call("session.stop", { session: "s" })]);
    assert.equal(requests, 6);
  } finally { globalThis.fetch = original; }
});
