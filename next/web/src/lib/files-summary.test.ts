import { test } from "node:test";
import assert from "node:assert/strict";
import { filesSummary } from "./files-summary.ts";

test("the file summary explains failed and saving uploads", () => {
  assert.equal(filesSummary([{ state: "ready" }, { state: "failed" }, { state: "saving" }]), "3 files · 1 not saved · 1 saving");
  assert.equal(filesSummary([{ state: "ready" }]), "1 file");
});
