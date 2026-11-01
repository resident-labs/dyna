import type { SessionStarted } from "./contract.gen.ts";
import { urgency, type Status } from "./status.ts";

export type TreeRow = { kind: "session"; session: SessionStarted; depth: number; parent: boolean; hidden: number; state: Status; dot: Status; mark?: string }
  | { kind: "idle"; key: string; depth: number; count: number; open: boolean };

type Summary = { size: number; top: Status; quiet: boolean; inside: Status };

const folds: Record<Status, string> = { waiting: "A folded session waits for you", failed: "A folded session failed", working: "A folded session is working", idle: "" };

// Index the tree once, then aggregate each subtree before emitting visible rows.
export function sessionTree(sessions: SessionStarted[], own: Map<string, Status>, starting: (id: string) => boolean,
  focus: string | undefined, folded: ReadonlySet<string>, openIdle: ReadonlySet<string>): TreeRow[] {
  const byId = new Map(sessions.map(item => [item.session, item]));
  const children = new Map<string | undefined, SessionStarted[]>();
  for (const item of sessions) {
    const parent = item.parent && byId.has(item.parent) ? item.parent : undefined;
    const siblings = children.get(parent) ?? [];
    siblings.push(item);
    children.set(parent, siblings);
  }
  const summary = new Map<string, Summary>();
  function aggregate(item: SessionStarted): Summary {
    let size = 1, inside: Status = "idle";
    let quiet = own.get(item.session) === "idle" && !starting(item.session);
    for (const child of children.get(item.session) ?? []) {
      const sub = aggregate(child);
      size += sub.size;
      if (urgency(sub.top) < urgency(inside)) inside = sub.top;
      quiet = quiet && sub.quiet;
    }
    const state = own.get(item.session) ?? "idle";
    const top = urgency(inside) < urgency(state) ? inside : state;
    const result = { size, top, quiet, inside };
    summary.set(item.session, result);
    return result;
  }
  for (const root of children.get(undefined) ?? []) aggregate(root);
  const subtree = (id: string): Summary => summary.get(id) ?? { size: 1, top: "idle", quiet: true, inside: "idle" };
  const keep = new Set<string>();
  for (let id = focus; id && byId.has(id) && !keep.has(id); id = byId.get(id)?.parent) keep.add(id);
  const rows: TreeRow[] = [];
  function walk(parent: string | undefined, depth: number): void {
    const kids = (children.get(parent) ?? []).sort((a, b) => {
      const x = subtree(a.session), y = subtree(b.session);
      return urgency(x.top) - urgency(y.top) || Number(x.quiet) - Number(y.quiet);
    });
    const idle = kids.filter(item => subtree(item.session).quiet && !keep.has(item.session));
    const idleIds = new Set(idle.map(item => item.session));
    const key = parent ?? "", open = idle.length < 2 || openIdle.has(key);
    for (const item of kids) {
      if (item === idle[0] && idle.length >= 2) rows.push({ kind: "idle", key, depth, count: idle.length, open });
      if (!open && idleIds.has(item.session)) continue;
      const sub = subtree(item.session);
      const hidden = folded.has(item.session) ? sub.size - 1 : 0;
      const state = own.get(item.session) ?? "idle";
      const shown = hidden && urgency(sub.inside) < urgency(state) ? sub.inside : state;
      rows.push({ kind: "session", session: item, depth, parent: sub.size > 1, hidden, state, dot: shown,
        mark: shown === state ? undefined : folds[shown] });
      if (!hidden) walk(item.session, depth + 1);
    }
  }
  walk(undefined, 0);
  return rows;
}
