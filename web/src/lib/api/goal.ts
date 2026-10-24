// Goals. Source: internal/goals/store.go.

export interface GoalEdit {
  at: string
  by: string
  oldText: string
}

export interface GoalOutcome {
  at: string
  by: string
  summary: string
}

// GoalAmendment is goals.Amendment: one attributed correction to the brief.
// The engine appends amendments, so stored order is oldest first.
export interface GoalAmendment {
  at: string
  by: string
  text: string
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
  edits: GoalEdit[] | null
  detail?: string
  amendments?: GoalAmendment[] | null
  service?: boolean
  expediteUntil?: string // omitted when not expedited; the room's one priority goal
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

// GoalItem is goals.Item in internal/goals/items.go.
export interface GoalItem {
  id: string
  text: string
  state: string
  updatedAt: string
  assignee?: string
}

// GoalStatus is goals.Status in internal/goals/status.go.
export interface GoalStatus {
  label: string
  reason: string
  done: number
  open: number
  dropped: number
}

// GoalEvent is goals.Event in internal/goals/journal.go. kind is always "event".
export interface GoalEvent {
  id: string
  goal: string
  by: string
  kind: string
  event: string
  at: string
  item?: string
  text?: string
}

// GoalRule is goals.Rule in internal/goals/rules.go. Its edits reuse GoalEdit,
// which has the same fields as goals.Edit.
export interface GoalRule {
  id: string
  text: string
  setter: string
  setterName: string
  createdAt: string
  updatedAt: string
  edits?: GoalEdit[]
}

// RulesView is goals.View in internal/goals/rules.go, the result of Rules.
export interface RulesView {
  rules: GoalRule[] | null
  used: number
  max: number
  hash: string
}
