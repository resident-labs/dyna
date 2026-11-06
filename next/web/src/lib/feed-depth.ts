import type { Principal, SessionStarted } from "./contract.gen.ts";

export type FeedDepth = "root" | "leads" | "all";
export type FeedDepthFilter = { mode: FeedDepth; depths: ReadonlyMap<string, number> };

// Only sessions connected to a known root have a depth. Missing ancestry is not a new root.
export function sessionDepths(sessions: Pick<SessionStarted, "session" | "parent">[]): Map<string, number> {
  const children = new Map<string, string[]>();
  const depths = new Map<string, number>();
  const queue: string[] = [];
  for (const { session, parent } of sessions) {
    if (!parent) { depths.set(session, 0); queue.push(session); }
    else children.set(parent, [...children.get(parent) ?? [], session]);
  }
  for (const parent of queue) {
    for (const child of children.get(parent) ?? []) {
      depths.set(child, depths.get(parent)! + 1);
      queue.push(child);
    }
  }
  return depths;
}

// Root is a from/to predicate, not a depth cutoff. Leads adds shallow conversations and fails
// open when any participant's ancestry is missing, even alongside a known deep participant.
export function showsPost(from: Principal, to: Principal[], filter?: FeedDepthFilter): boolean {
  if (!filter || filter.mode === "all") return true;
  const participants = [from, ...to];
  if (participants.some(person => person.kind === "owner" || (person.kind === "session" && person.session && filter.depths.get(person.session) === 0))) return true;
  if (filter.mode === "root") return false;
  let maximum = 0;
  for (const person of participants) {
    const depth = person.kind === "session" && person.session ? filter.depths.get(person.session) : undefined;
    if (depth === undefined) return true;
    maximum = Math.max(maximum, depth);
  }
  return maximum <= 1;
}

const prefix = "dyna.feed-depth:";
type DepthStorage = Pick<Storage, "getItem" | "setItem">;

// Storage is the untrusted boundary, and accessing it can itself throw when a browser blocks it.
export function loadFeedDepth(room: string, storage: () => DepthStorage): FeedDepth {
  try {
    const raw = storage().getItem(prefix + room);
    if (raw === "root" || raw === "leads") return raw;
  } catch {
    // Blocked storage uses All; choosing a value still works for this visit.
  }
  return "all";
}

export function saveFeedDepth(room: string, value: FeedDepth, storage: () => DepthStorage): void {
  try {
    storage().setItem(prefix + room, value);
  } catch {
    // A full or blocked store must not prevent changing the view.
  }
}
