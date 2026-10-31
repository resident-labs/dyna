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

test("groups changes with one nonblank context line on each side", () => {
  const before = "top\n\nold\n\nmiddle\n\nkeep\n\nlast\nend";
  const after = "top\n\nnew\n\nmiddle\n\nkeep\n\nchanged\nend";
  const hunks = instructionHunks(instructionDiff(before, after));
  assert.deepEqual(hunks.map(h => [h.line, h.lines.map(l => l.text)]), [
    [3, ["top", "old", "new", "middle"]],
    [9, ["keep", "last", "changed", "end"]],
  ]);
});
