import { test } from "node:test";
import assert from "node:assert/strict";
import { canForwardPost, replaceForward, undoForward } from "./forward-state.ts";

test("replace and Undo preserve the draft; failed posts never forward", () => {
  const first = { author: "A", at: "1", text: "one", attachments: [] };
  const second = { author: "B", at: "2", text: "two", attachments: [] };
  const replaced = replaceForward({ quote: first, draft: "typed draft" }, second);
  assert.deepEqual(replaced, { quote: second, previous: first, draft: "typed draft" });
  assert.deepEqual(undoForward(replaced), { quote: first, draft: "typed draft" });
  assert.equal(canForwardPost(true, false, "failed", 0), false);
  assert.equal(canForwardPost(false, true, "waiting", 0), false);
  assert.equal(canForwardPost(false, false, "", 0), false);
  assert.equal(canForwardPost(false, false, "", 1), true);
});
