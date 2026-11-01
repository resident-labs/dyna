import { test } from "node:test";
import assert from "node:assert/strict";
import { accountAlerts, balancesText, busiestWindow, hideLabel, holdFor, holding, limitReached, outOfLimit, usageRows, visibleWindows, windowLabel, windowMeter } from "./usage.ts";
import type { AccountUsage, AccountsListResponse, UsageWindow } from "./contract.gen.ts";

test("labels each window and hides only unused model-specific windows", () => {
  const five: UsageWindow = { name: "session", kind: "five_hour", usedPercent: 0, seconds: 18000, resetsAt: "" };
  const weekly: UsageWindow = { ...five, name: "week", kind: "weekly", seconds: 604800 };
  const fable: UsageWindow = { ...weekly, name: "fable", model: "Fable" };
  assert.equal(windowLabel(five), "5-hour");
  assert.equal(windowLabel(weekly), "Weekly");
  assert.equal(windowLabel(fable), "Weekly · Fable");
  assert.deepEqual(visibleWindows([five, weekly, fable, { ...fable, usedPercent: 0.1 }, { ...fable, usedPercent: 0.6 }]), [five, weekly, { ...fable, usedPercent: 0.6 }]);
  assert.equal(busiestWindow([{ title: "studio", windows: [{ ...fable, usedPercent: 80 }] }]), "studio · Weekly (Fable) 80% used");
});

test("hides a model-specific window that repeats the account's own, and keeps one that differs", () => {
  const month: UsageWindow = { name: "monthly", kind: "monthly", usedPercent: 99.86, seconds: 2592000, resetsAt: "2026-11-24T00:00:00Z" };
  const code: UsageWindow = { ...month, name: "Code monthly", model: "Code" };
  assert.deepEqual(visibleWindows([month, code]), [month]);
  assert.deepEqual(visibleWindows([{ ...month, usedPercent: 55 }, code]), [{ ...month, usedPercent: 55 }, code]);
  assert.deepEqual(visibleWindows([{ ...month, resetsAt: "2026-11-30T00:00:00Z" }, code]), [{ ...month, resetsAt: "2026-11-30T00:00:00Z" }, code]);
  assert.deepEqual(visibleWindows([code]), [code]);
});

const windowAt = (usedPercent: number): UsageWindow => ({ name: "week", kind: "weekly", usedPercent, seconds: 604800, resetsAt: "" });
const usageOf = (accountId: string, label: string, provider: string, windows: UsageWindow[] | null = [windowAt(10)], cause: AccountUsage["cause"] = ""): AccountUsage =>
  ({ accountId, provider, label, windows, cause });
const catalog: AccountsListResponse = {
  providers: [
    { id: "anthropic", displayName: "Claude", connectMethod: "oauth", connectFlow: "callback" },
    { id: "openai", displayName: "ChatGPT", connectMethod: "oauth", connectFlow: "callback" },
    { id: "kimi", displayName: "Kimi Code", connectMethod: "oauth", connectFlow: "device" },
    { id: "xai", displayName: "xAI", connectMethod: "oauth", connectFlow: "device" },
    { id: "opencode-go", displayName: "OpenCode Go", connectMethod: "key", connectFlow: "key" },
  ],
  accounts: (["anthropic", "openai", "kimi", "xai", "opencode-go"] as const).map(provider => ({ id: provider, provider, label: "", rooms: null, state: "connected" })),
};

test("titles an account by its label and adds the catalog's provider name only when the label does not say it", () => {
  const names = (label: string, id: string, proxy = "proxy-name") => usageRows([usageOf(id, label, proxy)], catalog).map(({ title, provider }) => [title, provider]);
  assert.deepEqual(names("pipsqueak.tasks", "anthropic", "claude"), [["pipsqueak.tasks", "Claude"]]);
  assert.deepEqual(names("chatgpt", "openai", "codex"), [["chatgpt", undefined]]);
  assert.deepEqual(names("kimi", "kimi"), [["kimi", undefined]]);
  assert.deepEqual(names("xai", "xai"), [["xai", undefined]]);
  assert.deepEqual(names("opencode-go", "opencode-go", "openai-compatible-opencode-go"), [["opencode-go", undefined]]);
  assert.deepEqual(names("Claude · studio", "anthropic"), [["Claude · studio", undefined]]);
  // A short label that is only part of the name does not say the provider.
  assert.deepEqual(names("go", "opencode-go"), [["go", "OpenCode Go"]]);
  // Without the catalog, or an account it lists, the label stands alone.
  assert.deepEqual(usageRows([usageOf("anthropic", "pipsqueak.tasks", "claude")]).map(row => row.provider), [undefined]);
  assert.deepEqual(names("orphan", "gone"), [["orphan", undefined]]);
  assert.deepEqual(names("", "anthropic"), [["Claude", undefined]]);
});

test("Usage keeps only accounts with reported windows, highest usage first, then by name", () => {
  const unusedModel = { ...windowAt(0), model: "Fable" };
  const rows = usageRows([
    usageOf("a", "quiet", "kimi", []),
    usageOf("b", "null", "xai", null),
    usageOf("c", "unused-model", "claude", [unusedModel]),
    usageOf("d", "expired", "claude", [windowAt(99)], "sign_in_expired"),
    usageOf("e", "beta", "claude", [windowAt(40)]),
    usageOf("f", "alpha", "claude", [windowAt(5), windowAt(40)]),
    usageOf("g", "spent", "claude", [windowAt(0), windowAt(100)]),
    usageOf("h", "rounds-up", "claude", [windowAt(99.6)]),
    usageOf("i", "limited", "claude", [windowAt(30)], "rate_limited"),
    usageOf("j", "limited-empty", "claude", [], "rate_limited"),
    usageOf("k", "lost-empty", "claude", null, "unreachable"),
  ]);
  assert.deepEqual(rows.map(row => row.title), ["rounds-up", "spent", "alpha", "beta", "limited"]);
});

test("A balance keeps an account in Usage, as text after every account with windows", () => {
  const balance = { currency: "USD", amount: "3.50" };
  const rows = usageRows([
    { ...usageOf("a", "deepseek", "openai-compatible-deepseek", []), balances: [{ currency: "CNY", amount: "110.00" }, balance] },
    { ...usageOf("b", "unused", "claude", [windowAt(0)]) },
    { ...usageOf("c", "expired", "kimi", [], "sign_in_expired"), balances: [balance] },
  ]);
  assert.deepEqual(rows.map(row => row.title), ["unused", "deepseek"]);
  assert.equal(balancesText(rows[1].balances), "110.00 CNY · 3.50 USD");
  assert.equal(balancesText([{ currency: "USD", amount: "-0.01" }]), "-0.01 USD");
  assert.deepEqual(rows[0].balances, []);
});

test("An account is out of limit with a spent window or no credit in any balance", () => {
  const now = Date.parse("2026-10-31T00:00:00Z");
  const out = (windows: UsageWindow[], ...amounts: string[]) => outOfLimit({ windows, balances: amounts.map(amount => ({ currency: "USD", amount })) }, now);
  assert.equal(out([windowAt(40), windowAt(100)]), true);
  assert.equal(out([windowAt(99)]), false);
  assert.equal(out([], "-0.01"), true);
  assert.equal(out([], "0.00"), true);
  assert.equal(out([], "-0.01", "3.50"), false);
  assert.equal(out([], "3.50"), false);
  assert.equal(out([]), false);
});

test("Usage shows only active sign-ins, and an account the list does not know", () => {
  const list: AccountsListResponse = { providers: [], accounts: (["connected", "quota_low", "unknown", "failed"] as const).map(state => ({ id: state, provider: "anthropic", label: "", rooms: null, state })) };
  const rows = usageRows(["connected", "quota_low", "unknown", "failed", "new"].map(id => usageOf(id, id, "claude")), list);
  assert.deepEqual(rows.map(row => row.id).sort(), ["connected", "new", "quota_low"]);
});

test("A hidden account waits for its busiest window's reset, or a higher balance", () => {
  const now = Date.parse("2026-10-31T00:00:00Z");
  const quiet = { ...windowAt(5), resetsAt: "2026-10-31T02:00:00Z" }, busy = { ...windowAt(60), resetsAt: "2026-11-02T00:00:00Z" };
  const spend = (amount: string) => ({ windows: [], balances: [{ currency: "USD", amount }] });
  const reset = holdFor({ windows: [quiet, busy], balances: [] }, now);
  assert.deepEqual(reset, { until: busy.resetsAt });
  assert.equal(hideLabel(reset), "Hide until it resets");
  assert.equal(holding({ balances: [] }, reset, now), true);
  assert.equal(holding({ balances: [] }, reset, Date.parse(busy.resetsAt)), false);
  const credit = holdFor(spend("3.50"), now);
  assert.equal(hideLabel(credit), "Hide until topped up");
  assert.equal(holding(spend("2.00"), credit, now), true);
  assert.equal(holding(spend("3.50"), credit, now), true);
  assert.equal(holding(spend("4.00"), credit, now), false);
  // Go's zero time says nothing, so there is nothing to wait for: only the owner shows it again.
  const none = holdFor({ windows: [{ ...windowAt(0), resetsAt: "0001-01-01T00:00:00Z" }], balances: [] }, now);
  assert.deepEqual(none, {});
  assert.equal(hideLabel(none), "Hide");
  assert.equal(holding({ balances: [] }, none, now), true);
});

test("Names a monthly window", () => {
  assert.equal(windowLabel({ ...windowAt(5), kind: "monthly", seconds: 2592000 }), "Monthly");
});

test("Needs you lists only expired sign-ins, by name, and reconnects only those with OAuth", () => {
  const alerts = accountAlerts([
    usageOf("a", "fine", "claude"),
    usageOf("openai", "unreachable", "codex", [], "unreachable"),
    usageOf("anthropic", "zeta", "claude", [], "sign_in_expired"),
    usageOf("opencode-go", "key", "opencode-go", [], "sign_in_expired"),
    usageOf("xai", "limited", "xai", [], "rate_limited"),
  ], catalog);
  assert.deepEqual(alerts.map(({ id, title, provider, reconnect }) => ({ id, title, provider, reconnect })), [
    { id: "opencode-go", title: "key", provider: "OpenCode Go", reconnect: false },
    { id: "anthropic", title: "zeta", provider: "Claude", reconnect: true },
  ]);
  assert.deepEqual(accountAlerts([usageOf("a", "fine", "claude"), usageOf("b", "quiet", "kimi", [])], catalog), []);
});

test("a spent window says when it comes back, and an old reading claims nothing", () => {
  const now = Date.parse("2026-10-31T00:00:00Z");
  const spent = (resetsAt: string): UsageWindow => ({ ...windowAt(100), resetsAt });
  assert.deepEqual(windowMeter(spent("2026-11-01T14:00:00Z"), now), { label: "Weekly", percent: 100, unit: "used", busy: true, text: "Resets in 1\u00a0d\u00a014\u00a0h" });
  assert.equal(limitReached(spent("2026-11-01T14:00:00Z"), now), true);
  assert.equal(windowMeter(spent("0001-01-01T00:00:00Z"), now).text, "Limit reached");
  assert.equal(limitReached(spent("0001-01-01T00:00:00Z"), now), true);
  assert.equal(windowMeter(spent("2026-10-30T00:00:00Z"), now).text, "0% left");
  assert.equal(limitReached(spent("2026-10-30T00:00:00Z"), now), false);
  assert.equal(limitReached({ ...windowAt(99), resetsAt: "2026-11-01T14:00:00Z" }, now), false);
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

test("names the busiest usage window of any shown account by its title", () => {
  const w = (usedPercent: number, seconds: number): UsageWindow => ({ name: "", kind: seconds === 18000 ? "five_hour" : "weekly", usedPercent, seconds, resetsAt: "" });
  assert.equal(busiestWindow(usageRows([
    { accountId: "a", provider: "claude", label: "pipsqueak.tasks", cause: "", windows: [w(61.6, 18000), w(40, 604800)] },
    { accountId: "b", provider: "codex", label: "team", cause: "sign_in_expired", windows: [w(99, 604800)] },
    { accountId: "c", provider: "kimi", label: "spare", cause: "", windows: null }])), "pipsqueak.tasks · 5-hour 62% used");
  assert.equal(busiestWindow([]), undefined);
});
