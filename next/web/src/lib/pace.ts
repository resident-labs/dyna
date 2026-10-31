import type { UsageWindow } from "./contract.gen.ts";

export type PaceStatus = "ahead" | "on" | "under";
export const paceWords: Record<PaceStatus, string> = { ahead: "ahead of pace", on: "on pace", under: "under pace" };

export interface Pace {
  status: PaceStatus;
  expectedRemaining: number;
}

const MINUTE = 60_000;

function statusOf(gap: number): PaceStatus {
  return gap > 5 ? "ahead" : gap < -5 ? "under" : "on";
}

// Compare the used share with the elapsed share of the reported window.
export function paceOf(window: UsageWindow, now: number): Pace | null {
  const end = Date.parse(window.resetsAt);
  const length = window.seconds * 1000;
  if (!(length > 0) || !Number.isFinite(end) || end <= now) return null;
  const elapsed = Math.min(100, Math.max(0, ((length - (end - now)) / length) * 100));
  return { status: statusOf(window.usedPercent - elapsed), expectedRemaining: 100 - elapsed };
}

// Compare the mean gap across windows that have a pace.
export function poolPace(windows: readonly UsageWindow[], now: number): PaceStatus | null {
  let gap = 0, count = 0;
  for (const window of windows) {
    const pace = paceOf(window, now);
    if (!pace) continue;
    gap += window.usedPercent - (100 - pace.expectedRemaining);
    count++;
  }
  return count ? statusOf(gap / count) : null;
}

// Wake at the next minute or reset, whichever comes first.
export function meterWake(resetsAt: string | undefined, now: number): number {
  const end = Date.parse(resetsAt ?? "");
  if (!Number.isFinite(end) || end <= now) return NaN;
  return Math.min(end, (Math.floor(now / MINUTE) + 1) * MINUTE);
}
