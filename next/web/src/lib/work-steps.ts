import { count } from "./feed.ts";
import { stepTime } from "./steps.ts";

// A toggle reverses the turn's default: failures open, other turns stay folded.
export const foldOpen = (failed: boolean, toggled: boolean): boolean => failed !== toggled;

export const isLiveTurn = (turn: string, newest: string, running?: string): boolean => turn === newest && turn === running;

export function workStepSummary(started: string, ended: string, steps?: number): string {
  const duration = stepTime({ started, ended });
  return `Worked${duration ? ` ${duration}` : ""} · ${steps === undefined ? "Show steps" : count(steps, "step")}`;
}
