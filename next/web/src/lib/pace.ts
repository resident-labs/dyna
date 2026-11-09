import type { UsageWindow } from "./contract.gen.ts";

export type PaceStatus = "ahead" | "on" | "under";
export const paceWords: Record<PaceStatus, string> = { ahead: "ahead of pace", on: "on pace", under: "under pace" };

export type Pace = {
  status: PaceStatus;
  expectedRemaining: number;
}

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
