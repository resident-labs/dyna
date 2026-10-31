import { test } from "node:test";
import assert from "node:assert/strict";
import { beginSwipe, moveSwipe, releaseSwipe, ignoredSwipeTarget } from "./swipe.ts";

test("right swipe locks, resists, ticks once and releases at the threshold", () => {
  const start = beginSwipe(30, 100, 0, true, false, false)!;
  assert.equal(moveSwipe(start, 42, 110, 1, true, false).swipe?.dragging, false);
  assert.equal(moveSwipe(start, 40, 115, 1, true, false).swipe, null);
  assert.equal(moveSwipe(start, 100, 100, 501, true, false).swipe, null);
  const ready = moveSwipe(start, 102, 100, 2, true, false);
  assert.equal(ready.tick, true);
  assert.equal(releaseSwipe(ready.swipe!, 102), true);
  assert.equal(releaseSwipe(ready.swipe!, 90), false);
  const farther = moveSwipe(ready.swipe!, 1000, 100, 3, true, false);
  assert.equal(farther.distance, 96);
  assert.equal(farther.tick, false);
  assert.equal(moveSwipe(farther.swipe!, 100, 100, 4, false, false).swipe, null);
  assert.equal(beginSwipe(23, 0, 0, true, false, false), null);
  assert.equal(beginSwipe(30, 0, 0, false, false, false), null);
  assert.equal(beginSwipe(30, 0, 0, true, true, false), null);
  assert.equal(beginSwipe(30, 0, 0, true, false, true), null);
  assert.match(ignoredSwipeTarget, /pre/);
  assert.match(ignoredSwipeTarget, /contenteditable/);
});
