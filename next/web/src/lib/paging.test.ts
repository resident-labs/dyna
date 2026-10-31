import { test } from "node:test";
import assert from "node:assert/strict";
import { shouldPage } from "./paging.ts";

test("rows that overflow the view by at most the edge page only on the reader's own move", () => {
  for (const overflow of [1, 300, 600]) assert.deepEqual(shouldPage(0, 800 + overflow, 800, 600), { start: false, end: false });
  assert.deepEqual(shouldPage(0, 800 + 601, 800, 600), { start: true, end: false });
  assert.deepEqual(shouldPage(100, 1100, 800, 600, -50), { start: true, end: false });
  // The list follows a live row or keeps its anchor: scrollTop rises, so the start does not page.
  assert.deepEqual(shouldPage(300, 1100, 800, 600, 200), { start: false, end: true });
});
