// One status per session and per room, in one priority order, so the room header, the session panel and Home
// count the same way: what waits for the owner first, then a failure, then work. A session that waits for the
// owner is waiting, not working.
export type Status = "waiting" | "failed" | "working" | "idle";

export const statusOf = ({ waiting, failed, working }: { waiting: boolean; failed: boolean; working: boolean }): Status =>
  waiting ? "waiting" : failed ? "failed" : working ? "working" : "idle";

export type Counts = Record<Status, number> & { sessions: number };

export function countOf(all: Iterable<Status>): Counts {
  const counts: Counts = { sessions: 0, waiting: 0, failed: 0, working: 0, idle: 0 };
  for (const status of all) { counts.sessions++; counts[status]++; }
  return counts;
}
