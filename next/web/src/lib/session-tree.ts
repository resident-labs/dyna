import type { BoxState, Responsibility, ResponsibilityKind, SessionStarted } from "./contract.gen.ts";
import { count } from "./feed.ts";
import { urgency, type Status } from "./status.ts";

export type TreeRow = { kind: "session"; session: SessionStarted; depth: number; parent: boolean; hidden: number; state: Status; dot: Status; mark?: string }
  | { kind: "idle"; key: string; depth: number; count: number; open: boolean };

// The sessions under id, nearest first. A close takes them along, and a move cannot go under them.
export function descendants(sessions: SessionStarted[], id: string): string[] {
  const children = new Map<string, string[]>();
  for (const child of sessions) {
    if (child.parent) children.set(child.parent, [...(children.get(child.parent) ?? []), child.session]);
  }
  const found = [...(children.get(id) ?? [])];
  for (let i = 0; i < found.length; i++) found.push(...(children.get(found[i]) ?? []));
  return found;
}

// What a close costs, naming up to two of the sessions it takes along.
export function closeNote(names: string[]): string {
  if (!names.length) return "Its work is archived first.";
  const shown = names.slice(0, 2);
  const rest = names.length - shown.length;
  return `Also closes ${rest > 0 ? `${shown.join(", ")} and ${rest} more` : shown.join(" and ")}. Their work is archived first.`;
}

const holdingNames: Record<ResponsibilityKind, string> = {
  turn: "turn", shell_job: "shell job", terminal: "terminal", workflow: "workflow run", goal: "goal", pr: "PR", issue: "issue", question: "question", child: "child",
};

// What a session still holds, as the engine lists it. A running turn has its own row, so it is not repeated here.
export function holdsText(items: Responsibility[]): string {
  return items.filter(item => item.kind !== "turn").map(item => [holdingNames[item.kind], item.id].filter(Boolean).join(" ")).join(", ");
}

// What a close hands over, as "This hands 2 goals and 1 PR to root." Nothing to hand over needs no sentence.
export function handNote(handed: Responsibility[], to: string): string {
  const counts = new Map<ResponsibilityKind, number>();
  for (const item of handed) counts.set(item.kind, (counts.get(item.kind) ?? 0) + 1);
  const parts = [...counts].map(([kind, n]) => count(n, holdingNames[kind]));
  if (!parts.length) return "";
  const last = parts.pop()!;
  return `This hands ${parts.length ? `${parts.join(", ")} and ${last}` : last} to ${to}.`;
}

export type SessionAction = "move" | "stop" | "restart" | "close";

// The actions the engine takes for a session now. Root has no parent, so it stays where it is and stays open.
export function sessionActions(parent: string | undefined, stoppable: boolean, box?: BoxState): SessionAction[] {
  return [...(parent ? ["move"] as const : []), ...(stoppable ? ["stop"] as const : []), ...(box === "failed" ? ["restart"] as const : []), ...(parent ? ["close"] as const : [])];
}

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
