import test from "node:test";
import assert from "node:assert/strict";
import { recoverInstructionDraft } from "./instruction-draft.ts";

test("recovering a draft after another save keeps its original base for conflict review", () => {
  const recovered = recoverInstructionDraft(
    JSON.stringify({ text: "my edit", base: "original" }),
    "old", { text: "their edit", hash: "new" },
  );
  assert.deepEqual(recovered, { draft: "my edit", base: "original", hash: "old", stale: true });
});
