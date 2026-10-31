import { test } from "node:test";
import assert from "node:assert/strict";
import { meterWake, paceOf, poolPace } from "./pace.ts";

const now = Date.parse("2026-10-20T12:00:00Z");
const window = { name: "week", seconds: 7 * 86400, resetsAt: "2026-10-24T12:00:00Z", usedPercent: 43 };

test("pace compares used and elapsed shares within a five-point band", () => {
  const even = paceOf(window, now);
  assert.equal(even?.status, "on");
  assert.ok(Math.abs((even?.expectedRemaining ?? 0) - 57.142) < 0.01);
  assert.equal(paceOf({ ...window, usedPercent: 70 }, now)?.status, "ahead");
  assert.equal(paceOf({ ...window, usedPercent: 30 }, now)?.status, "under");
  assert.equal(paceOf({ ...window, seconds: 0 }, now), null);
  assert.equal(paceOf(window, Date.parse(window.resetsAt)), null);
});

test("pool pace averages gaps and skips windows without pace", () => {
  assert.equal(poolPace([{ ...window, usedPercent: 70 }, { ...window, usedPercent: 20 }], now), "on");
  assert.equal(poolPace([{ ...window, usedPercent: 70 }, { ...window, seconds: 0 }], now), "ahead");
  assert.equal(poolPace([{ ...window, seconds: 0 }], now), null);
});

test("meter wakes at the next minute or reset", () => {
  assert.equal(meterWake(window.resetsAt, now), now + 60_000);
  assert.equal(meterWake("2026-10-20T12:00:30Z", now), now + 30_000);
  assert.ok(Number.isNaN(meterWake(window.resetsAt, Date.parse(window.resetsAt))));
});
