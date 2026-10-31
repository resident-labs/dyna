import { test } from "node:test";
import { strict as assert } from "node:assert";
import { reloadDecision } from "./reload.ts";

test("a changed build reloads only when no work would be lost", () => {
  assert.equal(reloadDecision(false, false, false), "reload");
  assert.equal(reloadDecision(true, false, false), "notice");
  assert.equal(reloadDecision(false, true, false), "notice");
  assert.equal(reloadDecision(false, false, true), "notice");
});
