import type { SharedEntry } from "./shared.ts";

export type SharedRow = { kind: "entry"; path: string; parent: string; depth: number; entry: SharedEntry } | { kind: "empty" | "loading" | "error"; path: string; depth: number };
export const sharedChild = (parent: string, name: string) => parent ? `${parent}/${name}` : name;
export const sharedRowKey = (row: SharedRow) => `${row.kind}:${row.path}`;

// Flatten only expanded folders for the shared virtual list, never the whole filesystem.
export function sharedRows(listings: ReadonlyMap<string, SharedEntry[]>, expanded: ReadonlySet<string>, failures: ReadonlySet<string>): SharedRow[] {
  const rows: SharedRow[] = [];
  function visit(parent: string, depth: number) {
    const entries = listings.get(parent);
    if (failures.has(parent)) rows.push({ kind: "error", path: parent, depth });
    else if (!entries) rows.push({ kind: "loading", path: parent, depth });
    else if (!entries.length) rows.push({ kind: "empty", path: parent, depth });
    else for (const entry of entries) {
      const path = sharedChild(parent, entry.name);
      rows.push({ kind: "entry", path, parent, depth, entry });
      if (entry.dir && expanded.has(path)) visit(path, depth + 1);
    }
  }
  visit("", 0);
  return rows;
}
