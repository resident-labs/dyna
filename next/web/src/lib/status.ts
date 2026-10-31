// One status per session, in one priority order, so the room header and the session panel count the same way. A box
// that failed or was lost comes first, since approving a request does not repair it. Then what waits for the owner,
// then a running turn: a new turn moves on from an earlier failed turn the owner has not seen yet, which comes next.
// A session that waits for the owner is waiting, not working.
export type Status = "waiting" | "failed" | "working" | "idle";

// The priority order as a number for sorting: 0 is the most urgent.
export const urgency = (status: Status): number => ["failed", "waiting", "working", "idle"].indexOf(status);

export const statusOf = ({ broken, waiting, working, failed }: { broken: boolean; waiting: boolean; working: boolean; failed: boolean }): Status =>
  broken ? "failed" : waiting ? "waiting" : working ? "working" : failed ? "failed" : "idle";

export type Counts = Record<Status, number> & { sessions: number };

export function countOf(all: Iterable<Status>): Counts {
  const counts: Counts = { sessions: 0, waiting: 0, failed: 0, working: 0, idle: 0 };
  for (const status of all) { counts.sessions++; counts[status]++; }
  return counts;
}
