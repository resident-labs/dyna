// What the Pull requests list shows beside the rows: today's merges and the
// next row for the j and k keys. The functions are pure; GitHub.svelte draws
// the result.
import type { ChangeView } from '../api/change.ts'

// The time a merged change was last seen. A change has no merge time of its
// own, and the watcher stops polling a PR once it merges.
const seenAt = (c: Partial<Pick<ChangeView, 'checked'>>): number => Date.parse(c.checked || '')

// mergedToday keeps the merged changes seen since local midnight.
export function mergedToday<T extends Partial<Pick<ChangeView, 'state' | 'checked'>>>(rows: T[] | null | undefined, now = new Date()): T[] {
  const midnight = new Date(now).setHours(0, 0, 0, 0)
  return (rows ?? []).filter((c) => c.state === 'merged' && seenAt(c) >= midnight)
}

// neighbor is the row next to the open one, for the j and k keys. It stays
// on the first or last row at the edge.
export function neighbor<T extends Partial<Pick<ChangeView, 'changeId'>>>(rows: T[], id: string, step: number): T | null {
  if (!rows.length) return null
  const at = rows.findIndex((c) => c.changeId === id)
  return rows[Math.min(Math.max(at + step, 0), rows.length - 1)] ?? null
}
