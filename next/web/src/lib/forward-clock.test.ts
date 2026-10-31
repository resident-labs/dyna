import { test } from "node:test";
import assert from "node:assert/strict";
import { ForwardExpiry, type ForwardClock } from "./forward-clock.ts";

test("Undo expiry is five seconds, independent per conversation, and typing cancels it", () => {
  let next = 0;
  const timers = new Map<number, { callback: () => void; delay: number }>();
  const clock: ForwardClock = {
    set: (callback, delay) => { const id = ++next; timers.set(id, { callback, delay }); return id; },
    clear: id => { timers.delete(id); },
  };
  const expired: string[] = [], expiry = new ForwardExpiry(key => expired.push(key), clock);
  expiry.start("room/a"); expiry.start("room/b");
  assert.deepEqual([...timers.values()].map(timer => timer.delay), [5000, 5000]);
  expiry.clear("room/a");
  assert.equal(timers.size, 1);
  timers.get(2)!.callback();
  assert.deepEqual(expired, ["room/b"]);
});
