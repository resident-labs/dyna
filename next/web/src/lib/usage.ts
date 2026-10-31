import type { UsageCause, UsageWindow } from "./contract.gen.ts";
import type { Meter } from "./hosts.ts";

export const causeText: Record<Exclude<UsageCause, "">, { pill: string; line: string }> = {
  sign_in_expired: { pill: "Sign-in expired", line: "Sign in again to see its limits." },
  unreachable: { pill: "Unreachable", line: "Dyna could not read its limits." },
};

const count = (n: number, unit: string) => `${n} ${unit}${n === 1 ? "" : "s"}`;

// A window's span from its length: "5 hours", "7 days", "1 day", "30 minutes".
function span(seconds: number): string {
  return seconds % 86400 === 0 ? count(seconds / 86400, "day") : seconds % 3600 === 0 ? count(seconds / 3600, "hour") : count(Math.round(seconds / 60), "minute");
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
