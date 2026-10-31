import { test } from "node:test";
import assert from "node:assert/strict";
import { ruleParts, splitRule } from "./rules.ts";

test("a rule splits only where a sentence ends", () => {
  assert.deepEqual(splitRule("Tests are fast. Reviewers reject slow ones."), { lead: "Tests are fast.", rest: " Reviewers reject slow ones." });
  assert.deepEqual(splitRule("Read r17. Then stop."), { lead: "Read r17. Then stop.", rest: "" });
  assert.deepEqual(splitRule("Use a fake, e.g. Vitest mocks, and stop. Never sleep."), { lead: "Use a fake, e.g. Vitest mocks, and stop.", rest: " Never sleep." });
  assert.deepEqual(splitRule("One sentence, ending here."), { lead: "One sentence, ending here.", rest: "" });
});

test("a number links only when that rule is active and the number is not code", () => {
  const active = new Set([17]);
  assert.deepEqual(ruleParts("See r17, r3 and `r17`.", active), [
    { kind: "text", text: "See " }, { kind: "link", text: "r17", number: 17 }, { kind: "text", text: ", r3 and " },
    { kind: "code", text: "r17" }, { kind: "text", text: "." },
  ]);
  assert.deepEqual(ruleParts("Keep src/r17/a.go, 'r17' and r17.go as they are.", active), [{ kind: "text", text: "Keep src/r17/a.go, 'r17' and r17.go as they are." }]);
});
