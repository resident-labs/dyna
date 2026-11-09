import { test } from "node:test";
import assert from "node:assert/strict";
import { goalTree, openLeaves } from "./goals.ts";

test("a goal's items nest under their parents and count open leaves", () => {
  const items = [
    { number: 1, text: "port", state: "open" as const },
    { number: 2, text: "docs", state: "done" as const },
    { number: 3, parent: 1, text: "tests", state: "open" as const },
    { number: 4, parent: 1, text: "lint", state: "dropped" as const },
    { number: 5, parent: 9, text: "orphan", state: "open" as const },
  ];
  assert.deepEqual(goalTree(items).map(node => [node.number, node.subtasks.map(sub => sub.number)]), [[1, [3, 4]], [2, []], [5, []]]);
  assert.equal(openLeaves(items), 2);
});
