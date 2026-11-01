import { test } from "node:test";
import assert from "node:assert/strict";
import { staleWidthFailures } from "../../known-width-failures.ts";

test("fixed route widths must leave the known-failures list", () => {
  const baseline = { "/settings/profiles": [768, 920], "/": [390] };
  assert.deepEqual(staleWidthFailures(baseline, new Set(["/settings/profiles 768", "/ 390"])), ["/settings/profiles 920"]);
});
