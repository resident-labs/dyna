import { test } from "node:test";
import assert from "node:assert/strict";
import { ScrollHold } from "./scrollHold.ts";

test("scroll corrections wait for quiet and flush once", () => {
  const hold = new ScrollHold();
  hold.activity(100);
  hold.add(40);
  hold.activity(200);
  hold.add(-10);
  assert.equal(hold.take(349), 0);
  assert.equal(hold.take(350), 30);
  assert.equal(hold.take(351), 0);
});
