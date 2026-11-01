import type { AccountProviderInfo, AccountUsage, AccountsListResponse, UsageCause, UsageWindow } from "./contract.gen.ts";
import type { Meter } from "./hosts.ts";

// An expired sign-in needs the owner to act, so its row says only that; the others say why Dyna has no reading.
export const causeText: Record<Exclude<UsageCause, "">, { pill: string; line?: string; tone?: "warning" }> = {
  sign_in_expired: { pill: "Sign-in expired", tone: "warning" },
  rate_limited: { pill: "Rate limited", line: "The provider asked Dyna to try again later." },
  unreachable: { pill: "Unreachable", line: "Dyna could not read its limits." },
};

const count = (n: number, unit: string) => `${n} ${unit}${n === 1 ? "" : "s"}`;

// A window's length in its largest whole unit: [5, "hour"], [7, "day"], [30, "minute"].
const unitOf = (seconds: number): [number, string] =>
  seconds % 86400 === 0 ? [seconds / 86400, "day"] : seconds % 3600 === 0 ? [seconds / 3600, "hour"] : [Math.round(seconds / 60), "minute"];

// A window's span from its length: "5 hours", "7 days", "1 day", "30 minutes".
const span = (seconds: number) => count(...unitOf(seconds));

// The catalog's entry for a usage account. Quota names a provider as the proxy does ("codex"), so the account ID joins it to accounts.list.
function providerOf(account: AccountUsage, catalog?: AccountsListResponse): AccountProviderInfo | undefined {
  const provider = catalog?.accounts?.find(a => a.id === account.accountId)?.provider;
  return catalog?.providers?.find(p => p.id === provider);
}

const squash = (text: string) => text.toLowerCase().replace(/[^\p{L}\p{N}]/gu, "");

// Whether a provider's name tells the owner more than the label does. "pipsqueak.tasks" does not say Claude;
// "opencode-go" says OpenCode Go, "Claude · studio" says Claude and "kimi" says Kimi Code.
function addsToLabel(label: string, name: string): boolean {
  return !squash(label).includes(squash(name)) && squash(label) !== squash(name.split(" ")[0]);
}

// An account's name on screen: its label, with the provider's display name when that adds to it.
function named(account: AccountUsage, catalog?: AccountsListResponse) {
  const info = providerOf(account, catalog);
  const name = info?.displayName;
  return { title: account.label || name || "Model account", provider: name && account.label && addsToLabel(account.label, name) ? name : undefined, info };
}

export type UsageRow = { title: string; provider?: string; windows: UsageWindow[] };

// The accounts that report usage, highest figure first, then by name, so the order holds between loads.
// An account with a cause is an alert instead, and one with neither says nothing, so it is left out.
export function usageRows(accounts: AccountUsage[], catalog?: AccountsListResponse): UsageRow[] {
  const peak = (row: UsageRow) => Math.max(...row.windows.map(usedOf));
  return accounts.flatMap(account => {
    const windows = account.cause ? [] : visibleWindows(account.windows ?? []);
    if (!windows.length) return [];
    const { title, provider } = named(account, catalog);
    return [{ title, provider, windows }];
  }).sort((a, b) => peak(b) - peak(a) || a.title.localeCompare(b.title));
}

export type AccountAlert = { id: string; title: string; provider?: string; cause: (typeof causeText)[keyof typeof causeText]; reconnect: boolean };

// The accounts that go in Needs you: those with a cause, expired sign-ins first, then by name. Only a sign-in that
// expired on a provider with OAuth can be reconnected.
export function accountAlerts(accounts: AccountUsage[], catalog?: AccountsListResponse): AccountAlert[] {
  return accounts.flatMap(account => {
    if (!account.cause) return [];
    const { title, provider, info } = named(account, catalog);
    return [{ id: account.accountId ?? "", title, provider, cause: causeText[account.cause], reconnect: account.cause === "sign_in_expired" && info?.connectMethod === "oauth" }];
  }).sort((a, b) => Number(!a.cause.tone) - Number(!b.cause.tone) || a.title.localeCompare(b.title));
}

// The busiest window by account, such as "pipsqueak.tasks · 5-hour 62% used"; none without one.
export function busiestWindow(rows: UsageRow[]): string | undefined {
  const windows = rows.flatMap(({ title, windows }) => windows.map(w => ({ title, w })));
  const top = windows.reduce<(typeof windows)[number] | undefined>((max, next) => !max || next.w.usedPercent > max.w.usedPercent ? next : max, undefined);
  if (!top) return undefined;
  const label = windowLabel({ ...top.w, model: undefined });
  return `${top.title} · ${label}${top.w.model ? ` (${top.w.model})` : ""} ${Math.round(top.w.usedPercent)}% used`;
}

// "2 h 10 min", "3 d 4 h" or "10 min" until a time, with no-break spaces so it wraps as one; none when it is not
// ahead, such as the zero time a provider sends for an unused window.
function until(at: string, now: number): string | undefined {
  const minutes = Math.round((Date.parse(at) - now) / 60_000);
  if (!(minutes >= 1)) return undefined;
  const [d, h, m] = [Math.floor(minutes / 1440), Math.floor(minutes / 60) % 24, minutes % 60];
  return (d ? `${d} d ${h} h` : h ? `${h} h ${m} min` : `${m} min`).replaceAll(" ", "\u00a0");
}

// A typed kind names the period; a model-specific limit names its model too.
export function windowLabel(w: UsageWindow): string {
  const period = w.kind === "five_hour" ? "5-hour" : w.kind === "weekly" ? "Weekly" : w.seconds > 0 ? span(w.seconds) : "Usage";
  return w.model ? `${period} · ${w.model}` : period;
}

export function visibleWindows(windows: UsageWindow[]): UsageWindow[] {
  return windows.filter(w => !w.model || Math.round(w.usedPercent) > 0);
}

const usedOf = (w: UsageWindow) => Math.min(100, Math.max(0, Math.round(w.usedPercent)));

// Whether a window has nothing left. A reset time already past means an old reading, so that claims nothing.
export function limitReached(w: UsageWindow, now: number): boolean {
  const resets = Date.parse(w.resetsAt);
  return usedOf(w) >= 100 && !(resets > 0 && resets <= now);
}

// A usage window as a meter: a label, percent used, and time to reset; a spent window says when it comes back.
// It is busy at 90% used or more.
export function windowMeter(w: UsageWindow, now: number): Meter {
  const used = usedOf(w);
  const resets = until(w.resetsAt, now);
  const text = !limitReached(w, now) ? `${100 - used}% left` + (resets ? ` · ${resets}` : "") : resets ? `Resets in ${resets}` : "Limit reached";
  return { label: windowLabel(w), percent: used, unit: "used", busy: used >= 90, text };
}
