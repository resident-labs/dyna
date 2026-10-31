import test from "node:test";
import assert from "node:assert/strict";
import { instructionDiff } from "./instruction-diff.ts";

test("shows separate additions and removals in file order", () => {
  const changes = instructionDiff("one\ntwo\nthree", "one\nchanged\nthree\nfour").filter(line => line.kind !== "same");
  assert.deepEqual(changes, [
    { kind: "remove", text: "two", line: 2 },
    { kind: "add", text: "changed", line: 2 },
    { kind: "add", text: "four", line: 4 },
  ]);
});
