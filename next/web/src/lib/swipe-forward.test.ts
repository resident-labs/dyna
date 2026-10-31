import { test } from "node:test";
import assert from "node:assert/strict";
import { SwipeForward } from "./swipe-forward.ts";

test("navigating away cancels the pending forward, while a retained row forwards", () => {
  let next = 0;
  const timers = new Map<number, () => void>();
  const forward = new SwipeForward({
    set: callback => { const id = ++next; timers.set(id, callback); return id; },
    clear: id => { timers.delete(id); },
  });
  const drafts: string[] = [];
  forward.start(() => drafts.push("old conversation"), 150);
  forward.cancel(); // The old conversation's row is destroyed on navigation.
  for (const callback of timers.values()) callback();
  assert.deepEqual(drafts, []);
  forward.start(() => drafts.push("current conversation"), 150);
  timers.get(2)!();
  assert.deepEqual(drafts, ["current conversation"]);
});
