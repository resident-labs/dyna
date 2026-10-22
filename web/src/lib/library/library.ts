// Pure helpers for the read-only Library view.
import type { LibraryEntry, LibraryView } from '../api/library.ts'

export interface TreeRow {
  entry: LibraryEntry
  depth: number
}

export interface LinkTarget {
  path: string
  dir: boolean
}

// libraryTree orders live entries as a tree: each entry follows its parent,
// siblings sort by name. An entry whose parent is missing or retired shows
// at the top level, so no live entry disappears.
export function libraryTree(entries: LibraryEntry[]): TreeRow[] {
  const live = entries.filter((e) => !e.retired)
  const ids = new Set(live.map((e) => e.id))
  const kids = new Map<string, LibraryEntry[]>()
  for (const e of live) {
    const key = e.parent && ids.has(e.parent) ? e.parent : ''
    kids.set(key, [...(kids.get(key) ?? []), e])
  }
  const rows: TreeRow[] = []
  const walk = (key: string, depth: number) => {
    for (const entry of (kids.get(key) ?? []).sort((a, b) => a.name.localeCompare(b.name))) {
      rows.push({ entry, depth })
      walk(entry.id, depth + 1)
    }
  }
  walk('', 0)
  return rows
}

// linkTarget is where a link opens in the file viewer. A glob opens the
// directory before its first wildcard segment, or the repository root;
// any other link opens that file.
export function linkTarget(link: string): LinkTarget {
  const parts = link.split('/')
  const at = parts.findIndex((p) => /[*?[{]/.test(p))
  return at < 0 ? { path: link, dir: false } : { path: parts.slice(0, at).join('/'), dir: true }
}

// isStale reports whether an entry id is in the view's stale list.
export const isStale = (view: Pick<LibraryView, 'stale'> | null, id: string): boolean => (view?.stale ?? []).includes(id)
