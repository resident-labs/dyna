import { parent, treeRows, type Listing, type TreeEntry } from './repo.ts'

// Search cached names only. Matching descendants keep their ancestors visible,
// including when those ancestors were collapsed before filtering.
export function filterLoaded(lists: Readonly<Record<string, Listing | undefined>>, query: string): TreeEntry[] {
  const term = query.trim().toLocaleLowerCase()
  const keep = new Set<string>()
  const expanded = new Set<string>()
  for (const list of Object.values(lists)) {
    for (const entry of list?.entries ?? []) {
      if (!entry.name.toLocaleLowerCase().includes(term)) continue
      keep.add(entry.path)
      for (let path = parent(entry.path); path; path = parent(path)) {
        keep.add(path)
        expanded.add(path)
      }
    }
  }
  return treeRows(lists, expanded).filter((row): row is TreeEntry => row.kind !== 'note' && keep.has(row.path))
}
