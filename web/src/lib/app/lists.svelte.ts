import { mergeListPage } from '../ui/listPages.ts'
import { patchListRows } from '../ui/listChanges.ts'
import { reconcileRows } from '../ui/reloadGate.ts'
import type { ListPageView } from '../ui/listPages.ts'
import type { RowChange } from '../ui/listChanges.ts'
import type { ListChange } from '../api/event.ts'
import type { Goal } from '../api/goal.ts'
import type { ShellJob } from '../api/terminal.ts'
import type { Row as ChangeRow } from '../prs/prlist.ts'

export interface ListPage<T> {
  items: T[] | null
  older?: boolean
  cursor?: string
  limit?: number
  loadedOlder?: boolean
}
interface ListKinds {
  goals: Goal
  changes: ChangeRow
  jobs: ShellJob
}
export type ListKind = keyof ListKinds
type ListPages = { [K in ListKind]?: ListPage<ListKinds[K]> }

export interface ListsHost {
  current(): { room: string; visit: number }
  patch(kind: 'goals' | 'changes', load: (current: () => boolean) => Promise<void>): Promise<void>
  goals(room: string, cursor?: string): Promise<ListPage<Goal>>
  changes(room: string, cursor?: string): Promise<ListPage<ChangeRow>>
  jobs(cursor?: string): Promise<void>
  setGoals(rows: Goal[]): void
  setChanges(rows: ChangeRow[]): void
  error(message: string): void
}

const missing = (): never => { throw new Error('lists: host is unbound') }
let listHost: ListsHost = {
  current: missing, patch: missing, goals: missing, changes: missing, jobs: missing,
  setGoals: missing, setChanges: missing, error: missing,
}
export function bindLists(host: ListsHost): void { listHost = host }

let listPages = $state<ListPages>({})
let olderLoading = $state(false)
let olderFailed = $state(false)

function applyListPage<K extends ListKind>(kind: K, page: ListPage<ListKinds[K]>, append = false): ListKinds[K][] {
  const previous = listPages[kind]
  const merged = mergeListPage(previous as ListPageView<ListKinds[K]> | undefined, { ...page, items: page.items ?? [], older: page.older ?? false, cursor: page.cursor ?? '' }, append)
  if (kind === 'jobs') merged.items.sort((a, b) => (String('started' in b ? b.started : '')).localeCompare(String('started' in a ? a.started : '')))
  merged.items = reconcileRows(previous?.items ?? [], merged.items, kind === 'changes' ? 'key' : 'id')
  listPages = { ...listPages, [kind]: merged }
  return merged.items
}

function applyListItem<K extends ListKind>(kind: K, change: ListChange, rows: ListKinds[K][] | null): ListKinds[K][] {
  const previous = listPages[kind] ?? { items: rows ?? [] }
  const items = patchListRows(previous.items ?? [], change as RowChange<ListKinds[K]>, kind === 'changes' ? 'key' : 'id')
  if (kind === 'jobs') items.sort((a, b) => (String('started' in b ? b.started : '')).localeCompare(String('started' in a ? a.started : '')))
  listPages = { ...listPages, [kind]: { ...previous, items } }
  return items
}

async function loadOlderLists(kinds: ListKind[]) {
  if (olderLoading) return
  const { room: id, visit } = listHost.current()
  olderLoading = true
  olderFailed = false
  try {
    for (const kind of kinds) {
      const page = listPages[kind]
      if (!page?.older) continue
      if (kind === 'jobs') {
        await listHost.jobs(page.cursor)
        continue
      }
      await listHost.patch(kind, async (current) => {
        const selected = listPages[kind]
        if (!current() || id !== listHost.current().room || visit !== listHost.current().visit || !selected?.older) return
        if (kind === 'goals') {
          const next = await listHost.goals(id, selected.cursor)
          if (!current() || id !== listHost.current().room || visit !== listHost.current().visit) return
          listHost.setGoals(applyListPage('goals', next, true))
        } else {
          const next = await listHost.changes(id, selected.cursor)
          if (!current() || id !== listHost.current().room || visit !== listHost.current().visit) return
          listHost.setChanges(applyListPage('changes', next, true))
        }
      })
    }
  } catch (e) {
    const now = listHost.current()
    if (id === now.room && visit === now.visit) { listHost.error(String(e)); olderFailed = true }
  } finally { olderLoading = false }
}

export const listView = {
  get pages() { return listPages },
  get olderLoading() { return olderLoading },
  get olderFailed() { return olderFailed },
  clear() { listPages = {} },
}

export { applyListPage, applyListItem, loadOlderLists }
