import assert from "node:assert/strict";
import { test } from "node:test";
import { addedLines, checkAdded, typeTokens } from "./check-ui-guard.ts";

const tokens = typeTokens("--t-cap: 11px; --t-ui: 13px; --t-body: 14px;");

// A page file after the change, and the diff that added lines 3, 4, 7 and 10.
const page = [
  "<style>",
  "  .old { font-size: 12px; }",
  "  .new { font-size: 13px; }",
  "  .why { font-size: 9px; } /* ui-guard: allow a printed label */",
  "  .panel { padding: 4px;",
  "    background: var(--side); }",
  "  .panel .x { padding: 2px; }",
  "  .card {",
  "    border: 1px solid var(--line); }",
  "  .group { background: transparent; }",
  "</style>",
].join("\n");
const diff = [
  "diff --git a/web/src/lib/x/Page.svelte b/web/src/lib/x/Page.svelte",
  "--- a/web/src/lib/x/Page.svelte",
  "+++ b/web/src/lib/x/Page.svelte",
  "@@ -2,0 +3,2 @@",
  "+  .new { font-size: 13px; }",
  "+  .why { font-size: 9px; } /* ui-guard: allow a printed label */",
  "@@ -4,0 +7 @@",
  "+  .panel .x { padding: 2px; }",
  "@@ -5,0 +8,3 @@",
  "+  .card {",
  "+    border: 1px solid var(--line); }",
  "+  .group { background: transparent; }",
].join("\n");

test("flags only added literal sizes and new private card styles, and honors the opt-out", () => {
  const added = addedLines(diff).get("web/src/lib/x/Page.svelte");
  assert.deepEqual([...(added ?? [])], [3, 4, 7, 8, 9, 10]);
  const found = checkAdded("web/src/lib/x/Page.svelte", page, added ?? new Set(), tokens);
  assert.deepEqual(found.map((v) => [v.line, v.rule]), [[3, "literal-font-size"], [8, "private-card-style"]]);
  assert.match(found[0]?.message ?? "", /use var\(--t-ui\) \(13px\)/);
  assert.match(found[1]?.message ?? "", /"\.card" \(border\); use Card from lib\/ui\/Card\.svelte/);
  // The shared parts may style their own cards.
  assert.deepEqual(checkAdded("web/src/lib/ui/Page.svelte", page, added ?? new Set(), tokens).map((v) => v.rule), ["literal-font-size"]);
});
