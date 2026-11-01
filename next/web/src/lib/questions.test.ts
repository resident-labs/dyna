import { test } from "node:test";
import assert from "node:assert/strict";
import { questionPlan } from "./questions.ts";

test("a question says what its session does meanwhile, or why it cannot, and an old one says nothing", () => {
  assert.equal(questionPlan({ meanwhile: "The tests" }), "Meanwhile: The tests");
  assert.equal(questionPlan({ meanwhile: "Every step needs the key", blocked: true }), "Blocked: Every step needs the key");
  assert.equal(questionPlan({}), "");
});
