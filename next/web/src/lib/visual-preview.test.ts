import assert from "node:assert/strict";
import test from "node:test";
import { contentRows, declaredFit, previewFit } from "./visual-preview.ts";

// Rows of one color each; a row given as two colors is its left half, then its right half.
function pixels(width: number, rows: (number | [number, number])[]): Uint8ClampedArray {
  return new Uint8ClampedArray(rows.flatMap(row => Array.from({ length: width }, (_, x) => {
    const value = typeof row === "number" ? row : row[x < width / 2 ? 0 : 1];
    return [value, value, value, 255];
  }).flat()));
}

test("content ends at the last row that differs from the page's background", () => {
  assert.equal(contentRows(pixels(4, [200, [20, 200], 20, 20, 20]), 4, 5), 2);
  // Compression noise is not content.
  assert.equal(contentRows(pixels(4, [200, 23, 18, 20, 20]), 4, 5), 1);
  assert.equal(contentRows(pixels(4, [20, 20, 20]), 4, 3), 0);
});

test("a bottom row of mixed colors is content past the capture, even when the rows above match it", () => {
  assert.equal(contentRows(pixels(4, [20, [20, 200], [20, 200], [20, 200]]), 4, 4), 4);
});

test("a short preview fits its content with a margin, and only clipped content fades", () => {
  assert.deepEqual(previewFit(45, 90), { height: 212, clipped: false });
  assert.deepEqual(previewFit(0, 90), { height: 120, clipped: false });
  assert.deepEqual(previewFit(89, 90), { height: 360, clipped: false });
  assert.deepEqual(previewFit(90, 90), { height: 360, clipped: true });
});

test("a live preview trusts only a height below the capture's", () => {
  assert.deepEqual(declaredFit(200), { height: 232, clipped: false });
  assert.deepEqual(declaredFit(360), { height: 360, clipped: true });
  assert.deepEqual(declaredFit(900), { height: 360, clipped: true });
});
