import test from "node:test";
import assert from "node:assert/strict";
import { instructionDiff, instructionHunks } from "./instruction-diff.ts";

test("shows separate additions and removals in file order", () => {
  const changes = instructionDiff("one\ntwo\nthree", "one\nchanged\nthree\nfour").filter(line => line.kind !== "same");
  assert.deepEqual(changes, [
    { kind: "remove", text: "two", line: 2 },
    { kind: "add", text: "changed", line: 2 },
    { kind: "add", text: "four", line: 4 },
  ]);
});

test("handles large documents and bounds unrelated changes", () => {
  const lines = Array.from({ length: 5000 }, (_, i) => `line ${i}`);
  const changed = lines.slice();
  changed[2500] = "edited";
  const hunks = instructionHunks(instructionDiff(lines.join("\n"), changed.join("\n")));
  assert.deepEqual(hunks.map(h => [h.line, h.lines.map(l => l.text)]), [
    [2501, ["line 2499", "line 2500", "edited", "line 2501"]],
  ]);

  const replacement = lines.map((_, i) => `replacement ${i}`);
  const diff = instructionDiff(lines.join("\n"), replacement.join("\n"));
  assert.equal(diff.length, 10000);
  assert.deepEqual(diff[0], { kind: "remove", text: "line 0", line: 1 });
  assert.deepEqual(diff[5000], { kind: "add", text: "replacement 0", line: 1 });
  assert.deepEqual(diff.at(-1), { kind: "add", text: "replacement 4999", line: 5000 });
});

test("groups changes with one nonblank context line on each side", () => {
  const before = "top\n\nold\n\nmiddle\n\nkeep\n\nlast\nend";
  const after = "top\n\nnew\n\nmiddle\n\nkeep\n\nchanged\nend";
  const hunks = instructionHunks(instructionDiff(before, after));
  assert.deepEqual(hunks.map(h => [h.line, h.lines.map(l => l.text)]), [
    [3, ["top", "old", "new", "middle"]],
    [9, ["keep", "last", "changed", "end"]],
  ]);
});
