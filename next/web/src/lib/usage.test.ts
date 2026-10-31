import { test } from "node:test";
import assert from "node:assert/strict";
import { busiestWindow, causeText, providerName, visibleWindows, windowLabel, windowMeter } from "./usage.ts";
import type { AccountProviderInfo, UsageWindow } from "./contract.gen.ts";

const providers: AccountProviderInfo[] = [{ id: "anthropic", displayName: "Claude", connectMethod: "oauth", connectFlow: "callback", flows: ["callback"], models: [] }];

test("labels each window and hides only unused model-specific windows", () => {
  const five: UsageWindow = { name: "session", kind: "five_hour", usedPercent: 0, seconds: 18000, resetsAt: "" };
  const weekly: UsageWindow = { ...five, name: "week", kind: "weekly", seconds: 604800 };
  const fable: UsageWindow = { ...weekly, name: "fable", model: "Fable" };
  assert.equal(windowLabel(five), "5-hour");
  assert.equal(windowLabel(weekly), "Weekly");
  assert.equal(windowLabel(fable), "Weekly · Fable");
  assert.deepEqual(visibleWindows([five, weekly, fable, { ...fable, usedPercent: 0.1 }, { ...fable, usedPercent: 0.6 }]), [five, weekly, { ...fable, usedPercent: 0.6 }]);
  assert.equal(busiestWindow([{ provider: "anthropic", label: "private", cause: "", windows: [{ ...fable, usedPercent: 80 }] }], providers), "Claude · Weekly (Fable) 80% used");
});

test("unreachable usage is muted while expired sign-in needs attention", () => {
  assert.equal(causeText.unreachable.tone, undefined);
  assert.equal(causeText.sign_in_expired.tone, "warning");
});

test("names a usage account by its provider, never by its label", () => {
  assert.equal(providerName("anthropic", providers), "Claude");
});

test("words a usage window by its span, percent used, percent left and time to reset; busy at 90% used", () => {
  const now = Date.parse("2026-10-31T00:00:00Z");
  assert.deepEqual(windowMeter({ name: "five_hour", kind: "five_hour", usedPercent: 61.6, seconds: 18000, resetsAt: "2026-10-31T02:10:00Z" }, now),
    { label: "5-hour", percent: 62, unit: "used", busy: false, text: "38% left · 2\u00a0h\u00a010\u00a0min" });
  assert.deepEqual(windowMeter({ name: "seven_day", kind: "weekly", usedPercent: 91, seconds: 604800, resetsAt: "2026-11-03T04:00:00Z" }, now),
    { label: "Weekly", percent: 91, unit: "used", busy: true, text: "9% left · 3\u00a0d\u00a04\u00a0h" });
  // An unused window comes with Go's zero time: no reset part, and a span from seconds, never the raw name.
  assert.deepEqual(windowMeter({ name: "one_day", kind: "other", usedPercent: 0, seconds: 86400, resetsAt: "0001-01-01T00:00:00Z" }, now),
    { label: "1 day", percent: 0, unit: "used", busy: false, text: "100% left" });
});

test("names the busiest usage window of any account by its provider, skipping accounts with a cause", () => {
  const w = (usedPercent: number, seconds: number): UsageWindow => ({ name: "", kind: seconds === 18000 ? "five_hour" : "weekly", usedPercent, seconds, resetsAt: "" });
  assert.equal(busiestWindow([
    { provider: "anthropic", label: "me@example.com", cause: "", windows: [w(61.6, 18000), w(40, 604800)] },
    { provider: "openai", label: "team", cause: "sign_in_expired", windows: [w(99, 604800)] },
    { provider: "kimi-coding", label: "spare", cause: "", windows: null }], providers), "Claude · 5-hour 62% used");
  assert.equal(busiestWindow([], providers), undefined);
});
