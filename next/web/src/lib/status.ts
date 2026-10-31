import { call } from "./client.ts";
import type { Event, FailureCause } from "./contract.gen.ts";

// One status per session, in one priority order, so the room header and the session panel count the same way. A box
// that failed or was lost comes first, since approving a request does not repair it. Then what waits for the owner,
// then a running turn: a new turn moves on from an earlier failed turn the owner has not seen yet, which comes next.
// A session that waits for the owner is waiting, not working.
export type Status = "waiting" | "failed" | "working" | "idle";

// The priority order as a number for sorting: 0 is the most urgent.
export const urgency = (status: Status): number => ["failed", "waiting", "working", "idle"].indexOf(status);

export const statusOf = ({ broken, waiting, working, failed }: { broken: boolean; waiting: boolean; working: boolean; failed: boolean }): Status =>
  broken ? "failed" : waiting ? "waiting" : working ? "working" : failed ? "failed" : "idle";

// A box that needs the owner. A failed box keeps its open turn, since session.restart resumes it, so an open turn does not mean work.
export type BoxFailure = { box: "failed" | "lost"; cause?: FailureCause; at?: string };

// session.list gives box states but not why a box failed or waits, so that comes from each session's newest box.changed.
// One page is enough, as boxes change seldom. A failed read leaves the reasons unknown.
export async function lastBoxChanges(room: string, beforeSeq: number): Promise<Map<string, Extract<Event, { type: "box.changed" }>>> {
  const page = await call("events.list", { room, afterSeq: 0, beforeSeq, limit: 200, types: ["box.changed"] }).catch(() => undefined);
  const found = new Map<string, Extract<Event, { type: "box.changed" }>>();
  for (const event of page?.events ?? []) {
    if (event.type === "box.changed" && event.seq > (found.get(event.body.session)?.seq ?? 0)) found.set(event.body.session, event);
  }
  return found;
}

export async function boxFailures(room: string, beforeSeq: number): Promise<Map<string, BoxFailure & { seq: number }>> {
  const found = new Map<string, BoxFailure & { seq: number }>();
  for (const [session, { seq, at, body: { to, cause } }] of await lastBoxChanges(room, beforeSeq)) {
    if (to === "failed" || to === "lost") found.set(session, { box: to, cause, at, seq });
  }
  return found;
}

// session.list does not say which jobs failed, so this reads the newest job ends once.
export async function failedJobs(room: string, beforeSeq: number): Promise<{ session: string; job: string; seq: number }[]> {
  const page = await call("events.list", { room, afterSeq: 0, beforeSeq, limit: 200, types: ["shell.job.ended"] }).catch(() => undefined);
  return (page?.events ?? []).flatMap(event => event.type === "shell.job.ended" && event.body.exitCode !== 0 ? [{ session: event.body.session, job: event.body.job, seq: event.seq }] : []);
}

// session.list says which last turns failed but not when, so this reads turn.ended pages back from the newest until it
// has placed each failure, at most 5 pages, so a long room never waits on its whole history. A session's newest
// turn.ended is its failure, since its last outcome failed. One it cannot place, as when a read fails, stays out.
export async function turnFailures(room: string, beforeSeq: number, sessions: Iterable<string>): Promise<Map<string, Extract<Event, { type: "turn.ended" }>>> {
  const unplaced = new Set(sessions);
  const placed = new Map<string, Extract<Event, { type: "turn.ended" }>>();
  for (let before = beforeSeq, pages = 0; unplaced.size && pages < 5; pages++) {
    const page = await call("events.list", { room, afterSeq: 0, beforeSeq: before, limit: 200, types: ["turn.ended"] }).catch(() => undefined);
    const events = page?.events ?? [];
    for (const event of events) {
      if (event.type === "turn.ended" && unplaced.has(event.body.session) && event.seq > (placed.get(event.body.session)?.seq ?? 0)) placed.set(event.body.session, event);
    }
    for (const session of placed.keys()) unplaced.delete(session);
    if (events.length < 200) break;
    before = Math.min(...events.map(event => event.seq));
  }
  return placed;
}

export type Counts = Record<Status, number> & { sessions: number };

export function countOf(all: Iterable<Status>): Counts {
  const counts: Counts = { sessions: 0, waiting: 0, failed: 0, working: 0, idle: 0 };
  for (const status of all) { counts.sessions++; counts[status]++; }
  return counts;
}
