import { test } from "node:test";
import assert from "node:assert/strict";
import { firstFrameMs, LiveStream } from "./live.ts";

test("a live view connects until its first frame, fails when none arrives in time, and ignores a closed stream", () => {
  const timers: { ms: number; run: () => void; cancelled: boolean }[] = [];
  let phase = "";
  const view = new LiveStream(next => { phase = next; }, (ms, run) => { const timer = { ms, run, cancelled: false }; timers.push(timer); return () => { timer.cancelled = true; }; });
  const first = view.start();
  assert.equal(phase, "connecting");
  first.frame();
  assert.deepEqual([phase, timers[0].ms, timers[0].cancelled], ["live", firstFrameMs, true]);
  // A retry closes the first image, whose cleared source fires load and error after the new stream starts.
  view.stop();
  view.start();
  first.frame();
  first.fail();
  assert.deepEqual([phase, timers[1].cancelled], ["connecting", false]);
  // With no frame, the new stream's timer fails the view.
  timers[1].run();
  assert.equal(phase, "failed");
});
