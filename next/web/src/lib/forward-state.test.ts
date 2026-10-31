import { test } from "node:test";
import assert from "node:assert/strict";
import { canForwardPost, replaceForward, undoForward } from "./forward-state.ts";

test("replace and Undo restore the previous post; failed posts never forward", () => {
  const first = { author: "A", at: "1", text: "one", attachments: [] };
  const second = { author: "B", at: "2", text: "two", attachments: [] };
  const state = { quote: first };
  assert.equal(replaceForward(state, first), state);
  const replaced = replaceForward(state, second);
  assert.deepEqual(replaced, { quote: second, previous: first });
  assert.deepEqual(undoForward(replaced), { quote: first });
  assert.equal(canForwardPost(true, false, "failed", 0), false);
  assert.equal(canForwardPost(false, true, "waiting", 0), false);
  assert.equal(canForwardPost(false, false, "", 0), false);
  assert.equal(canForwardPost(false, false, "", 1), true);
});
