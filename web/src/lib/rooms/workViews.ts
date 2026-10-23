// The sidebar's Work and Machine rows. A row with more than one tab shows
// the tabs in the header; each tab keeps its own view id, so links and the
// saved last view still name one page. A row opens its first tab.
export interface Tab {
  id: string
  label: string
  hidden?: boolean
}

export interface Row {
  id: string
  label: string
  hint: string
  tabs: Tab[]
  repo?: boolean
  terminal?: boolean
  computers?: boolean
}

export interface Section {
  label: string
  rows: Row[]
}

export const sections: Section[] = [
  {
    label: 'Work',
    rows: [
      { id: 'questions', label: 'questions', hint: 'Questions agents ask while they keep working', tabs: [{ id: 'questions', label: 'Questions' }] },
      { id: 'goals', label: 'goals', hint: 'Room goals', tabs: [{ id: 'goals', label: 'Goals' }] },
      { id: 'runs', label: 'runs', hint: 'Workflow runs', tabs: [{ id: 'runs', label: 'Workflows' }, { id: 'jobs', label: 'Shell jobs', hidden: true }] },
      { id: 'services', label: 'services', hint: 'What each service is doing, what it just did, and what runs next', tabs: [{ id: 'services', label: 'Services' }] },
      { id: 'visuals', label: 'visuals', hint: 'Every HTML card in this room', tabs: [{ id: 'visuals', label: 'Visuals' }] },
    ],
  },
  {
    label: 'Machine',
    rows: [
      { id: 'code', label: 'code', hint: 'CI health', repo: true, tabs: [{ id: 'cihealth', label: 'CI health' }] },
      { id: 'library', label: 'library', hint: 'The feature map of this repository', repo: true, tabs: [{ id: 'library', label: 'Library' }] },
      { id: 'terminal', label: 'terminal', hint: 'A shell on the host', terminal: true, tabs: [{ id: 'terminal', label: 'Terminal' }] },
      { id: 'computers', label: 'computers', hint: 'Every agent computer in this room', computers: true, tabs: [{ id: 'computers', label: 'Computers' }] },
    ],
  },
]

// rowOf is the sidebar row that holds a view, or undefined for the feed.
export function rowOf(view: string): Row | undefined {
  for (const s of sections) for (const r of s.rows) if (r.tabs.some((t) => t.id === view)) return r
}

// shown filters rows by what the room and this client have.
export const shown = (rows: Row[], { repo = false, terminal = false, computers = false }: { repo?: boolean; terminal?: boolean; computers?: boolean }): Row[] => rows.filter((r) => (!r.repo || repo) && (!r.terminal || terminal) && (!r.computers || computers))

export const runningJob = (j: { state: string }): boolean => j.state === 'running' || j.state === 'starting'

// runningWork is the gold count on the runs row: running workflow runs
// (the server's count). Shell jobs show in a session's Shell tab, not here.
export const runningWork = (runs: { running?: number } | null | undefined): number => runs?.running ?? 0

// visibleTabs is the tabs a row shows in the header, or null for one tab.
// A hidden tab only keeps its view under the row, such as a shell job log.
export function visibleTabs(row: Row | undefined): Tab[] | null {
  const tabs = row?.tabs.filter((t) => !t.hidden)
  return tabs && tabs.length > 1 ? tabs : null
}
