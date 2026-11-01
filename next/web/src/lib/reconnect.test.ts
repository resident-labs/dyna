import assert from "node:assert/strict";
import { test } from "node:test";
import { reconnectNotice } from "./reconnect.ts";
import type { Schedule } from "./stream.ts";

// fakeSchedule holds each timer until the test fires it, and records its delay.
function fakeSchedule() {
  const timers = new Map<number, () => void>();
  const delays: number[] = [];
  const schedule: Schedule = (callback, delay) => {
    const id = delays.push(delay);
    timers.set(id, callback);
    return () => { timers.delete(id); };
  };
  return {
    schedule,
    delays,
    pending: () => timers.size,
    fire: () => { for (const [id, callback] of [...timers]) { timers.delete(id); callback(); } },
  };
}

test("the reconnecting notice waits two seconds, so a short drop shows nothing", () => {
  const shown: boolean[] = [];
  const timer = fakeSchedule();
  const up = reconnectNotice(on => shown.push(on), timer.schedule);
  up(false);
  assert.deepEqual(timer.delays, [2000]);
  assert.deepEqual(shown, []);
  up(true);
  assert.equal(timer.pending(), 0, "the stream is back, so the wait ends");
  assert.deepEqual(shown, []);
});

test("a long drop shows the notice once, and hides it when the stream returns", () => {
  const shown: boolean[] = [];
  const timer = fakeSchedule();
  const up = reconnectNotice(on => shown.push(on), timer.schedule);
  up(false);
  up(false);
  assert.deepEqual(timer.delays, [2000], "a failed retry during the wait does not restart the timer");
  timer.fire();
  up(false);
  assert.deepEqual(shown, [true]);
  up(true);
  assert.deepEqual(shown, [true, false]);
});
