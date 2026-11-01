import { test } from "node:test";
import assert from "node:assert/strict";
import { matchKnownFailure, staleWidthFailures } from "../../known-width-failures.ts";

test("only the exact known issue is allowed and fixed entries must be removed", () => {
  const known = { fixture: "full", route: "/settings/profiles", width: 920, element: "div.table", text: "Profile ratings" } as const;
  assert.equal(matchKnownFailure([known], known), 0);
  assert.equal(matchKnownFailure([known], { ...known, fixture: "empty" }), -1);
  assert.equal(matchKnownFailure([known], { ...known, element: "button.save" }), -1);
  assert.equal(matchKnownFailure([known], { ...known, text: "Another issue" }), -1);
  assert.deepEqual(staleWidthFailures([known], new Set()), [known]);
  assert.deepEqual(staleWidthFailures([known], new Set([0])), []);
});
