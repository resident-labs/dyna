import { test } from "node:test";
import { deepStrictEqual } from "node:assert";
import { requestDecision } from "./request-decision.ts";

test("a device without full access sends no decision", async () => {
  const sent: string[] = [];
  const original = globalThis.fetch;
  globalThis.fetch = async input => { sent.push(String(input)); return new Response("{}"); };
  try {
    await requestDecision(false, { id: "request", approve: true });
    deepStrictEqual(sent, []);
    await requestDecision(true, { id: "request", approve: true });
    deepStrictEqual(sent, ["/v1/op.decide"]);
  } finally { globalThis.fetch = original; }
});
