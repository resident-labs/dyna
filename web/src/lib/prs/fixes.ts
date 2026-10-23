// Linked issues of a change. A PR that names issues closes them when it merges,
// so a row shows the link as people type it: "D-5, #207".
import type { IssueFix } from '../api/change.ts'

// fixLabel is one issue reference: D-5 for a room issue, #207 for GitHub.
export const fixLabel = (fix: IssueFix): string => fix.kind === 'dyna' ? `D-${fix.number}` : `#${fix.number}`

// fixesLabel joins a change's linked issues for one chip.
export const fixesLabel = (fixes?: readonly IssueFix[] | null): string => (fixes ?? []).map(fixLabel).join(', ')

// fixPRLabel names the open PR that fixes a room issue.
export const fixPRLabel = (pr?: number): string => pr ? `Fix in #${pr}` : ''
