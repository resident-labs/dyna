import { test } from "node:test";
import assert from "node:assert/strict";
import { firstFrameMs, LiveStream } from "./live.ts";

test("a live view connects until its first frame, and fails when no frame arrives in time", () => {
  const timers: { ms: number; run: () => void; cancelled: boolean }[] = [];
  let phase = "";
  const view = new LiveStream(next => { phase = next; }, (ms, run) => { const timer = { ms, run, cancelled: false }; timers.push(timer); return () => { timer.cancelled = true; }; });
  view.start();
  assert.equal(phase, "connecting");
  view.frame();
  assert.deepEqual([phase, timers[0].ms, timers[0].cancelled], ["live", firstFrameMs, true]);
  // A retry starts over; with no frame, its timer fails the view.
  view.start();
  timers[1].run();
  assert.equal(phase, "failed");
});
