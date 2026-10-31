import type { AccountUsage, UsageCause, UsageWindow } from "./contract.gen.ts";
import type { Meter } from "./hosts.ts";

// An expired sign-in needs attention; an unreachable usage reading is a muted fact.
export const causeText: Record<Exclude<UsageCause, "">, { pill: string; line: string; tone?: "warning" }> = {
  sign_in_expired: { pill: "Sign-in expired", line: "Sign in again to see its limits.", tone: "warning" },
  rate_limited: { pill: "Rate limited", line: "The provider asked Dyna to try again later." },
  unreachable: { pill: "Unreachable", line: "Dyna could not read its limits." },
};

const count = (n: number, unit: string) => `${n} ${unit}${n === 1 ? "" : "s"}`;

// A window's length in its largest whole unit: [5, "hour"], [7, "day"], [30, "minute"].
const unitOf = (seconds: number): [number, string] =>
  seconds % 86400 === 0 ? [seconds / 86400, "day"] : seconds % 3600 === 0 ? [seconds / 3600, "hour"] : [Math.round(seconds / 60), "minute"];

// A window's span from its length: "5 hours", "7 days", "1 day", "30 minutes".
const span = (seconds: number) => count(...unitOf(seconds));

// An account's name on screen: its provider, such as "Claude". The label is never shown, because it can be a sign-in address.
export const providerName = (provider: string) => `${provider.charAt(0).toUpperCase()}${provider.slice(1)}`;

// The busiest window of any account that reports one, as "Claude at 62% of the 5-hour window"; none without one.
export function busiestWindow(accounts: AccountUsage[]): string | undefined {
  const windows = accounts.flatMap(a => a.cause ? [] : (a.windows ?? []).map(w => ({ provider: a.provider, w })));
  const top = windows.reduce<(typeof windows)[number] | undefined>((max, next) => !max || next.w.usedPercent > max.w.usedPercent ? next : max, undefined);
  if (!top) return undefined;
  const [n, unit] = unitOf(top.w.seconds);
  return `${providerName(top.provider)} at ${Math.round(top.w.usedPercent)}% of the ${n}-${unit} window`;
}

// "2 h 10 min", "3 d 4 h" or "10 min" until a time, with no-break spaces so it wraps as one; none when it is not
// ahead, such as the zero time a provider sends for an unused window.
function until(at: string, now: number): string | undefined {
  const minutes = Math.round((Date.parse(at) - now) / 60_000);
  if (!(minutes >= 1)) return undefined;
  const [d, h, m] = [Math.floor(minutes / 1440), Math.floor(minutes / 60) % 24, minutes % 60];
  return (d ? `${d} d ${h} h` : h ? `${h} h ${m} min` : `${m} min`).replaceAll(" ", "\u00a0");
}

// A usage window as a meter: "5 hours", "62%" large with "used" after it, and "38% left · 2 h 10 min" until the reset, short enough for one line.
// It is busy at 90% used or more.
export function windowMeter(w: UsageWindow, now: number): Meter {
  const used = Math.min(100, Math.max(0, Math.round(w.usedPercent)));
  const resets = until(w.resetsAt, now);
  return { label: span(w.seconds), percent: used, unit: "used", busy: used >= 90, text: `${100 - used}% left` + (resets ? ` · ${resets}` : "") };
}
