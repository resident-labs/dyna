// The logic of the Settings list pages (spec 5.13, ui/settings): search, chips, sort and groups, the rows a window draws,
// and the keys. A page gives typed items and functions; nothing here reads a label to decide.

// A filter chip. The one with no test shows every item and no count.
export type Chip<T> = { id: string; label: string; test?: (item: T) => boolean };
// A column of a wide row: its head, its CSS grid track, and its order when its head sorts.
export type Column<T> = { id: string; label: string; width: string; compare?: (a: T, b: T) => number };
export type Sort<T> = { column: Column<T>; dir: 1 | -1 };
export type Group = { id: string; title: string };
export type Row<T> = { kind: "group"; group: Group; count: number; folded: boolean } | { kind: "item"; key: string; item: T };

export type ListQuery<T> = {
  text: (item: T) => string; key: (item: T) => string; query: string; chip: Chip<T>; chips?: Chip<T>[];
  sort?: Sort<T>; group?: (item: T) => Group; folded: ReadonlySet<string>;
};

// The rows to draw, how many items they hold (a folded group's included), and each chip's count over the search's matches.
export function listView<T>(items: readonly T[], { text, key, query, chip, chips = [], sort, group, folded }: ListQuery<T>): { rows: Row<T>[]; shown: number; counts: Record<string, number> } {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  const found = words.length ? items.filter(item => { const haystack = text(item).toLowerCase(); return words.every(word => haystack.includes(word)); }) : [...items];
  const counts = Object.fromEntries(chips.map(each => [each.id, each.test ? found.filter(each.test).length : found.length]));
  const shown = chip.test ? found.filter(chip.test) : found;
  if (sort?.column.compare) { const { compare } = sort.column; shown.sort((a, b) => compare(a, b) * sort.dir); }
  if (!group) return { rows: shown.map(item => ({ kind: "item", key: key(item), item })), shown: shown.length, counts };
  const groups = new Map<string, { group: Group; items: T[] }>();
  for (const item of shown) {
    const each = group(item);
    const entry = groups.get(each.id) ?? groups.set(each.id, { group: each, items: [] }).get(each.id)!;
    entry.items.push(item);
  }
  const rows: Row<T>[] = [];
  for (const { group: each, items: inside } of groups.values()) {
    // A search shows what it found, so it opens every group.
    const shut = !words.length && folded.has(each.id);
    rows.push({ kind: "group", group: each, count: inside.length, folded: shut });
    if (!shut) for (const item of inside) rows.push({ kind: "item", key: key(item), item });
  }
  return { rows, shown: shown.length, counts };
}

// Where each row starts, and the list's height. Rows have two fixed heights, an item's and a group head's.
export function layoutRows(rows: readonly Row<unknown>[], item: number, head: number): { tops: number[]; total: number } {
  let total = 0;
  const tops = rows.map(row => { const top = total; total += row.kind === "group" ? head : item; return top; });
  return { tops, total };
}

// The rows to draw for a scroll position: those in view and margin pixels more each way, from start up to end.
export function visibleRange(tops: readonly number[], top: number, height: number, margin: number): { start: number; end: number } {
  // The last row that starts at or above the window's top edge.
  let start = 0, last = tops.length - 1;
  while (start < last) { const middle = (start + last + 1) >> 1; if (tops[middle] <= top - margin) start = middle; else last = middle - 1; }
  let end = start;
  while (end < tops.length && tops[end] < top + height + margin) end++;
  return { start, end };
}

export type ListAction = { move: number } | { open: number } | { fold: number };

// What a key does on the list with the row at active (-1 for none) and page rows to a screen; undefined leaves the key alone.
export function listKey(key: string, active: number, rows: readonly Row<unknown>[], page: number): ListAction | undefined {
  if (!rows.length) return undefined;
  const step = ({ ArrowDown: 1, ArrowUp: -1, PageDown: page, PageUp: -page } as Record<string, number>)[key];
  if (step) return { move: active < 0 ? 0 : Math.max(0, Math.min(rows.length - 1, active + step)) };
  if (key === "Home") return { move: 0 };
  if (key === "End") return { move: rows.length - 1 };
  const row = rows[active];
  if (!row) return undefined;
  if (key === "Enter" || key === " ") return row.kind === "group" ? { fold: active } : { open: active };
  if (row.kind === "group" && ((key === "ArrowLeft" && !row.folded) || (key === "ArrowRight" && row.folded))) return { fold: active };
  return undefined;
}
