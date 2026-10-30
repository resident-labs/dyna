// Change wire types come from Go.
export type * from './change.gen.ts'
export type {
  Policy as ChangePolicy,
  Override as PolicyOverride,
  IssueRef as IssueFix,
  HeldPRComment as HeldComment,
  ReviewerSlotStatus as ReviewerSlot,
} from './change.gen.ts'

// ChangeDetail embeds an unexported Go struct. Tygo cannot generate that
// embedded type, so keep this response handwritten until it can.
import type { AuthorNotes, ChangeView, Finding, Override, Policy, ReviewRound } from './change.gen.ts'
export interface ChangeDetail extends ChangeView {
  summary: string
  notes: AuthorNotes
  findings: Finding[] | null
  reviewRounds: ReviewRound[] | null
  policy: Policy
  policyOverride: Override
  skipReasons: string[] | null
}
