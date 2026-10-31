import { test } from "node:test";
import assert from "node:assert/strict";
import { windowMeter } from "./usage.ts";

test("words a usage window by its span, percent used, percent left and time to reset; busy at 90% used", () => {
  const now = Date.parse("2026-10-31T00:00:00Z");
  assert.deepEqual(windowMeter({ name: "five_hour", usedPercent: 61.6, seconds: 18000, resetsAt: "2026-10-31T02:10:00Z" }, now),
    { label: "5 hours", percent: 62, unit: "used", busy: false, text: "38% left · 2\u00a0h\u00a010\u00a0min" });
  assert.deepEqual(windowMeter({ name: "seven_day", usedPercent: 91, seconds: 604800, resetsAt: "2026-11-03T04:00:00Z" }, now),
    { label: "7 days", percent: 91, unit: "used", busy: true, text: "9% left · 3\u00a0d\u00a04\u00a0h" });
  // An unused window comes with Go's zero time: no reset part, and a span from seconds, never the raw name.
  assert.deepEqual(windowMeter({ name: "one_day", usedPercent: 0, seconds: 86400, resetsAt: "0001-01-01T00:00:00Z" }, now),
    { label: "1 day", percent: 0, unit: "used", busy: false, text: "100% left" });
});
