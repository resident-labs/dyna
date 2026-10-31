import { test } from "node:test";
import assert from "node:assert/strict";
import { clamp, computerPlace, dragged, fitted, type Rect } from "./float.ts";

test("a floating window moves, resizes from any edge down to its minimum, and stays in the viewport", () => {
  const start: Rect = { x: 100, y: 100, w: 480, h: 300 };
  assert.deepEqual(dragged(start, [1, 1, 1, 1], 30, -20), { x: 130, y: 80, w: 480, h: 300 });
  assert.deepEqual(dragged(start, [0, 0, 1, 1], 40, 50), { x: 100, y: 100, w: 520, h: 350 });
  // The left edge stops at the minimum width; the right edge stays where it was.
  assert.deepEqual(dragged(start, [1, 0, 0, 0], 400, 0), { x: 260, y: 100, w: 320, h: 300 });
  assert.deepEqual(clamp({ x: 900, y: -40, w: 480, h: 300 }, 1024, 600), { x: 544, y: 0, w: 480, h: 300 });
  // A viewport smaller than the window shrinks it to fit.
  assert.deepEqual(clamp({ x: 10, y: 10, w: 480, h: 300 }, 400, 250), { x: 0, y: 0, w: 400, h: 250 });
});

test("fits a window to its frame, keeping the top right corner", () => {
  const rect = { x: 500, y: 68, w: 600, h: 400 };
  assert.deepEqual(fitted(rect, 1.6, 70, 800), { x: 500, y: 68, w: 600, h: 445 });
  // Too tall for the space above the composer, it narrows instead.
  assert.deepEqual(fitted(rect, 1.6, 70, 400), { x: 681, y: 68, w: 419, h: 332 });
});

test("a computer opens 16 px inside the feed's right edge, and says when it cannot clear the cards", () => {
  assert.deepEqual(computerPlace(1177, 68, 744), { rect: { x: 761, y: 68, w: 400, h: 300 }, clear: true });
  assert.equal(computerPlace(1168, 68, 744).clear, true);
  assert.equal(computerPlace(1167, 68, 744).clear, false);
});
