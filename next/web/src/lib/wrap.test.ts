import { test } from "node:test";
import assert from "node:assert/strict";
import { wrapParts } from "./wrap.ts";

test("a long token wraps at its seams", () => {
  assert.deepEqual(wrapParts("HORIZON_LAUNCHER_NATIVE_PROFILE"), ["HORIZON_", "LAUNCHER_", "NATIVE_", "PROFILE"]);
  assert.deepEqual(wrapParts("next/web/src/lib/ui/AboutPanel.svelte"), ["next/", "web/", "src/", "lib/", "ui/", "About", "Panel.", "svelte"]);
  assert.deepEqual(wrapParts("subscriptionRowsForTheRoom"), ["subscription", "Rows", "For", "The", "Room"]);
});

test("a URL wraps after its scheme and its slashes, not inside the two slashes", () => {
  assert.deepEqual(wrapParts("https://example.com/docs/page"), ["https://", "example.", "com/", "docs/", "page"]);
});

test("prose and short tokens stay one run", () => {
  assert.deepEqual(wrapParts("Ship the GitHub fix, then watch it."), ["Ship the GitHub fix, then watch it."]);
  assert.deepEqual(wrapParts("a_b and c/d"), ["a_b and c/d"]);
  assert.deepEqual(wrapParts(""), []);
});

test("a token in a sentence keeps the words around it", () => {
  const parts = wrapParts("Set HORIZON_LAUNCHER_NATIVE_PROFILE when it runs");
  assert.equal(parts.join(""), "Set HORIZON_LAUNCHER_NATIVE_PROFILE when it runs");
  assert.deepEqual(parts, ["Set HORIZON_", "LAUNCHER_", "NATIVE_", "PROFILE when it runs"]);
});
