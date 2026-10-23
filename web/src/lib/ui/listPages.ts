import type { Page } from '../api/wire.ts'

type Keyed = { key?: unknown; id?: unknown }
// The page a list keeps: the server page plus whether the user loaded older rows.
export interface ListPageView<T> extends Partial<Omit<Page<T>, 'items'>> {
  items: T[]
  loadedOlder?: boolean
}
// Callers hand over a page whose items are an array.
export type LoadedPage<T> = Omit<Page<T>, 'items'> & { items: T[] }

const key = (item: Keyed) => item.key ?? item.id

// The first list wins shared keys and sets their order. Later pages cannot
// replace newer rows that a current-page refresh already supplied.
function combine<T extends Keyed>(first: T[], second: T[]): T[] {
  const rows = new Map(first.map((item) => [key(item), item]))
  for (const item of second) if (!rows.has(key(item))) rows.set(key(item), item)
  return [...rows.values()]
}

// A refresh replaces page 1 until the user loads history. After that, keep
// the loaded rows and their continuation cursor until navigation resets it.
export function mergeListPage<T extends Keyed>(previous: ListPageView<T> | null | undefined, incoming: LoadedPage<T>, append = false): ListPageView<T> {
  if (!previous) return { ...incoming, loadedOlder: append }
  if (append) return { ...incoming, loadedOlder: true, items: combine(previous.items, incoming.items) }
  if (previous.loadedOlder) return { ...previous, items: combine(incoming.items, previous.items) }
  return { ...incoming, loadedOlder: false }
}
