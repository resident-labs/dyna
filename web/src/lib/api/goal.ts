// Goal wire types come from Go; aliases preserve the web API names.
export type * from './goal.gen.ts'
export type { Edit as GoalEdit, Outcome as GoalOutcome, Amendment as GoalAmendment, JournalLine as GoalJournalLine, JournalEntry as GoalJournalEntry, Item as GoalItem, Status as GoalStatus, Event as GoalEvent, Rule as GoalRule, View as RulesView } from './goal.gen.ts'
