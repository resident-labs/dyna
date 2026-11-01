import { test } from "node:test";
import { strict as assert } from "node:assert";
import { nameProblem } from "./name-problem.ts";

test("name rules match the engine", () => {
  for (const name of ["", "-a", "a b", "a".repeat(65), "root", "parent", "children", "siblings", "taken"]) assert.ok(nameProblem(name, ["taken"]), name);
  for (const name of ["a", "A._-9", "a".repeat(64), "Root", "Taken"]) assert.equal(nameProblem(name, ["taken"]), undefined, name);
});
