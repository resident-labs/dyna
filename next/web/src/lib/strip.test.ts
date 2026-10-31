import { test } from "node:test";
import assert from "node:assert/strict";
import { keyBytes, wholeTabs, withCtrl } from "./strip.ts";

test("the strip shows whole tabs in order, keeps the active one, and leaves the rest behind more", () => {
  assert.deepEqual(wholeTabs([100, 100, 100], 400, 0), [0, 1, 2]);
  assert.deepEqual(wholeTabs([100, 100, 100], 250, 0), [0, 1]);
  assert.deepEqual(wholeTabs([100, 100, 100], 250, 2), [0, 2]);
  assert.deepEqual(wholeTabs([300], 100, 0), [0]);
  assert.deepEqual(wholeTabs([], 100, 0), []);
});

test("the key row sends the escape, tab and arrow bytes, and a held Ctrl makes a control character", () => {
  assert.deepEqual([keyBytes("esc", false), keyBytes("tab", false), keyBytes("up", false), keyBytes("left", true)], ["\x1b", "\t", "\x1b[A", "\x1bOD"]);
  assert.deepEqual([withCtrl("c"), withCtrl("d"), withCtrl("ab"), withCtrl("1")], ["\x03", "\x04", "ab", "1"]);
});
