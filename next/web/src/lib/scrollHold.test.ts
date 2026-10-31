import { test } from "node:test";
import assert from "node:assert/strict";
import { ScrollHold } from "./scrollHold.ts";

test("scroll corrections wait for quiet and flush once", () => {
  const hold = new ScrollHold();
  hold.activity(100);
  hold.add(40);
  hold.activity(200);
  hold.add(-10);
  assert.equal(hold.active(349), true);
  hold.touch(true);
  assert.equal(hold.active(500), true);
  hold.touch(false);
  assert.equal(hold.active(500), false);
  assert.equal(hold.take(), 30);
  assert.equal(hold.take(), 0);
});
