import type { GoalItem } from "./contract.gen.ts";

// A goal's todo list as a tree: each top-level item with its subtasks, in number order. A subtask whose parent is missing
// shows at the top level.
export type GoalNode = GoalItem & { subtasks: GoalItem[] };

export function goalTree(items: readonly GoalItem[]): GoalNode[] {
  const top = new Map<number, GoalNode>();
  for (const item of items) if (!item.parent) top.set(item.number, { ...item, subtasks: [] });
  const out = [...top.values()];
  for (const item of items) {
    if (!item.parent) continue;
    const parent = top.get(item.parent);
    if (parent) parent.subtasks.push(item);
    else out.push({ ...item, subtasks: [] });
  }
  return out.sort((a, b) => a.number - b.number);
}

// The work left: open items with no open subtask, since an open parent of open subtasks is only their heading.
export const openLeaves = (items: readonly GoalItem[]): number =>
  items.filter(item => item.state === "open" && !items.some(sub => sub.parent === item.number && sub.state === "open")).length;
