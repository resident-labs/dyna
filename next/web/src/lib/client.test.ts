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
