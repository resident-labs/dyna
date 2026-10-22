// Goals. Source: internal/goals/store.go.

export interface GoalEdit {
  at: string
  by: string
  oldText: string
  waitUntil: string // zero time "0001-01-01T00:00:00Z" when unset
}

export interface GoalOutcome {
  at: string
  by: string
  summary: string
}

// Goal is goals.Goal. Go time.Time fields with omitempty are never omitted: an
// unset one is the zero time "0001-01-01T00:00:00Z".
export interface Goal {
  id: string
  room: string
  text: string
  setter: string
  holders: string[] | null
  shared: boolean
  parent: string
  state: string // "active" while open
  createdAt: string
  updatedAt: string
  waitAt: string // zero time "0001-01-01T00:00:00Z" when unset
  waitUntil: string // zero time when unset
  edits: GoalEdit[] | null
  detail?: string
  service?: boolean
  done?: GoalOutcome
}

// GoalJournalLine is goals.JournalLine in internal/goals/journal.go.
export interface GoalJournalLine {
  agent: string
  status: string
  summary: string
  goals: string[] | null
  done?: string[]
  needs?: string
}

// GoalJournalEntry is goals.JournalEntry in internal/goals/journal.go.
// GoalJournal returns Page<GoalJournalEntry>; nested nil slices stay null.
export interface GoalJournalEntry {
  id: string
  round?: string
  at: string
  entries: GoalJournalLine[] | null
  needsAction: boolean
}
