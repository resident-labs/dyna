// Pull request helpers for the PR chip, the session's PR panel and the
// feed's watcher rows. Colors keep the app's meaning: gold needs attention,
// sage is good, coral failed, quiet is waiting or over.
import type { ChangePolicy, ChangeView, CheckSummary, Finding, PolicyOverride } from '../api/change.ts'
import type { EventNotice } from '../api/room.ts'
import { durationMs } from '../ui/format.ts'
import type { Tone } from '../ui/tone.ts'

// A check as a row shows it. A GitHub check carries only a name, a state and a link.
export type CheckRow = Pick<CheckSummary, 'name'> & Partial<CheckSummary>
// A change row; callers pass a whole row or only the fields they read.
export type Facts = Omit<Partial<ChangeView>, 'checks'> & { checks?: CheckRow[] | null }
export interface Meta {
  label: string
  tone: string
}

// statusMeta gives the chip label and tone for a PR row from Changes.
export function statusMeta(pr?: Facts | null): Meta {
  if (pr?.isDraft && pr.state !== 'merged' && pr.state !== 'closed') return { label: 'Held', tone: 'faint' }
  const n = pr?.consecutiveFailures || 0
  const review = reviewMeta(pr)
  // A change from a local branch has no watcher status; its review state says where it stands.
  if (pr && review && !pr.status && pr.state === 'open') return review
  // CI and review run in parallel, so a row with CI running still shows its review.
  if (pr && review && ['ci-running', 'awaiting-review', 'approved', 'changes-requested', 'paused'].includes(pr.status ?? '') && !pr.ciPaused) return review
  switch (pr?.status) {
    case 'ci-running': return { label: 'CI running', tone: 'run' }
    case 'ci-failed': return { label: n ? `CI failed ${Math.min(n, 3)}/3` : 'CI failed', tone: 'coral' }
    case 'awaiting-review': return { label: 'In review', tone: 'quiet' }
    case 'changes-requested': return pr.verdict === 'question' ? { label: 'Question', tone: 'gold' } : { label: 'Changes requested', tone: 'gold' }
    case 'approved': return pr.verdict === 'approve-with-nits' ? { label: 'Approved with nits', tone: 'sage' } : { label: 'Approved', tone: 'sage' }
    case 'paused': return !pr.ciPaused && /reviewer/i.test(pr.pauseReason || '') ? { label: 'Needs a reviewer', tone: 'gold' } : { label: 'Paused', tone: 'gold' }
    case 'merged': return { label: 'Merged', tone: 'done' }
    case 'closed': return { label: 'Closed', tone: 'faint' }
    case 'untracked': return { label: 'Not tracked', tone: 'faint' }
    default: return { label: pr?.status || 'Tracked', tone: 'quiet' }
  }
}

// prAttention gives the row dot for a PR that needs someone to act: danger
// for a failure, warning for requested changes, a question or a block. Other
// states return null, so a row shows no dot.
export function prAttention(pr?: Facts | null): { tone: Tone; label: string } | null {
  if (!pr) return null
  const meta = statusMeta(pr)
  const tone: Tone | null = meta.tone === 'coral' ? 'danger' : meta.tone === 'gold' ? 'warning' : null
  return tone && { tone, label: `${changeName(pr)}: ${meta.label}` }
}

// prStory says in plain sentences where a PR stands, for the session
// profile: one sentence for review and one for CI. The profile labels each.
export function prStory(pr: Facts): { review: string; ci: string } {
  const tracks = prTracks(pr)
  const end = (s: string) => (/[.!?]$/.test(s) ? s : `${s}.`)
  const review = pr.isDraft ? 'The PR is a draft, so review waits.'
    : pr.status === 'paused' && pr.pauseReason ? end(`Paused: ${pr.pauseReason}`)
    : pr.reviewState === 'queued' ? (pr.queuePosition ? `Waiting for a reviewer, ${ordinal(pr.queuePosition)} in line.` : 'Waiting for a reviewer.')
    : pr.reviewState === 'reviewing' ? 'A reviewer is reading it now.'
    : pr.reviewState === 'approved' ? (tracks.ready ? 'Approved and ready to merge.' : 'Approved. It merges when CI passes.')
    : pr.reviewState === 'changes' ? (pr.verdict === 'question' ? 'The reviewer asked the author a question.' : 'The reviewer asked for changes.')
    : pr.reviewState === 'off' ? 'Automatic review is off.'
    : pr.reviewState === 'blocked' ? end(pr.reviewBlocked || 'Review is blocked')
    : end(statusMeta(pr).label)
  // The profile labels this line CI, so the sentence does not repeat it.
  const ci = tracks.ci === 'passed' ? 'Passed.'
    : tracks.ci === 'failed' ? end(pr.ciReason ? `Failed: ${pr.ciReason}` : 'Failed')
    : tracks.ci === 'running' ? 'Running.'
    : tracks.ci === 'absent' ? 'This repository has none.'
    : tracks.ci === 'local' ? 'Local CI checks this change.'
    : 'No result yet.'
  return { review, ci }
}

const ORDINALS = ['th', 'st', 'nd', 'rd']
// ordinal turns a queue position into "1st", "2nd", "11th".
export function ordinal(n: number): string {
  const v = n % 100
  return `${n}${ORDINALS[(v - 20) % 10] || ORDINALS[v] || ORDINALS[0]}`
}

// prTracks reads a change's two parallel tracks. The reviewer starts while
// CI still runs, so neither waits for the other. ready is true only when CI
// passed and the reviewer approved the change's current head. The chip, the
// list and the board all read their CI and review state from here.
export interface Tracks {
  ci: string
  approved: boolean
  held: boolean
  missing: string[]
  ready: boolean
}
export function prTracks(c?: Facts | null): Tracks {
  const ci = c?.state === 'merged' || c?.ci === 'passed' ? 'passed'
    : c?.ci === 'absent' ? 'absent'
    : c?.ci === 'failed' || c?.status === 'ci-failed' ? 'failed'
    : isLocal(c) ? 'local'
    : c?.ci === 'pending' || c?.status === 'ci-running' ? 'running' : 'unknown'
  const sameHead = !c?.reviewHead || !c?.head || c.reviewHead === c.head
  const approved = c?.reviewState === 'approved' && sameHead
  const held = !!c?.isDraft
  // The backend decides who is missing; the UI never claims ready while it lists anyone.
  const missing = c?.missingRequiredReviewers ?? []
  return { ci, approved, held, missing, ready: !held && !isLocal(c) && approved && ci === 'passed' && !missing.length }
}

// withCI adds the running CI track to a review label: "Reviewing · CI running".
export const withCI = (label: string, c: Facts): string => (prTracks(c).ci === 'running' ? `${label} · CI running` : label)

// reviewMeta says in plain words where a PR stands with the room's reviewer.
// It returns null for a row from a watcher that sends no reviewState.
export function reviewMeta(pr?: Facts | null): Meta | null {
  switch (pr?.reviewState) {
    case 'queued': return { label: withCI(pr.queuePosition ? `Queued for review (${ordinal(pr.queuePosition)})` : 'Queued for review', pr), tone: 'quiet' }
    case 'reviewing': return { label: withCI('Reviewing', pr), tone: 'iris' }
    case 'approved': {
      const nits = pr.verdict === 'approve-with-nits' ? 'Approved with nits' : 'Approved'
      return prTracks(pr).ready ? { label: `${nits}: ready to merge`, tone: 'sage' } : { label: withCI(nits, pr), tone: 'run' }
    }
    case 'changes': return pr.verdict === 'question' ? { label: 'Question for the author', tone: 'gold' } : { label: 'Changes requested', tone: 'gold' }
    case 'off': return { label: 'Review off', tone: 'faint' }
    case 'blocked': return { label: pr.reviewBlocked || 'Review blocked', tone: 'gold' }
    default: return null
  }
}

// The reviewer fields these helpers read; ReviewerStatus has them all.
export interface SlotLike {
  session?: { id?: string } | null
  current?: object | null
}
export interface ReviewerLike {
  reviewers?: SlotLike[] | null
  helpers?: { id: string }[] | null
}
export function reviewerSlots(r?: ReviewerLike | null): SlotLike[] {
  return r?.reviewers ?? []
}

// The room's reviewers show only on the PR screen. A reviewer session has the
// role marker "reviewer". The pool status also names them, and their helpers
// are the sessions below them.
export function withoutReviewers<T extends { id?: string; role?: string; parent?: string }>(members: T[] | null | undefined, reviewer: ReviewerLike | null = null): T[] {
  const ids = new Set([...reviewerSlots(reviewer).map((s) => s.session?.id), ...(reviewer?.helpers ?? []).map((h) => h.id)].filter((id) => !!id))
  const hidden = new Set((members ?? []).filter((m) => m.role === 'reviewer' || (m.id !== undefined && ids.has(m.id))).map((m) => m.id))
  // A session under a hidden one is hidden too; repeat until nothing changes.
  for (let grew = true; grew;) {
    grew = false
    for (const m of members ?? []) if (!hidden.has(m.id) && hidden.has(m.parent)) { hidden.add(m.id); grew = true }
  }
  return (members ?? []).filter((m) => !hidden.has(m.id))
}

// open says whether the watcher still acts on this PR.
export const isOpen = (pr?: Facts | null): boolean => !!pr && pr.state === 'open' && pr.status !== 'untracked'

// prsBySession maps a session id to its PRs, open ones first, newest first.
export function prsBySession<T extends Facts>(list: T[] | null | undefined): Record<string, T[]> {
  const out: Record<string, T[]> = {}
  for (const pr of list ?? []) {
    if (!pr.authorSession) continue
    ;(out[pr.authorSession] ||= []).push(pr)
  }
  for (const rows of Object.values(out)) rows.sort((a, b) => Number(isOpen(b)) - Number(isOpen(a)) || (b.number ?? 0) - (a.number ?? 0))
  return out
}

// checkMeta gives a CI check's state word and tone from GitHub's status and
// conclusion.
export interface CheckLook {
  word: string
  tone: string
}
export function checkMeta(c: Partial<CheckSummary>): CheckLook {
  const byState: Record<string, CheckLook> = { pending: { word: 'running', tone: 'run' }, success: { word: 'passed', tone: 'sage' }, failure: { word: 'failed', tone: 'coral' }, skipped: { word: 'skipped', tone: 'faint' }, cancelled: { word: 'cancelled', tone: 'faint' } }
  const known = c.state ? byState[c.state] : undefined
  if (known) return known
  if (c.status && c.status !== 'completed') return { word: c.status === 'queued' ? 'queued' : 'running', tone: 'run' }
  switch (c.conclusion) {
    case 'success': return { word: 'passed', tone: 'sage' }
    case 'failure': case 'timed_out': case 'startup_failure': return { word: c.conclusion === 'timed_out' ? 'timed out' : 'failed', tone: 'coral' }
    case 'cancelled': return { word: 'cancelled', tone: 'faint' }
    case 'skipped': case 'neutral': return { word: c.conclusion, tone: 'faint' }
    case 'action_required': return { word: 'needs action', tone: 'gold' }
    default: return { word: c.conclusion || 'unknown', tone: 'faint' }
  }
}

// checkTime is a check's run time: a local CI suite carries durationMs.
export function checkTime(c: Pick<CheckSummary, 'durationMs'>): string {
  return durationMs(c.durationMs ?? 0)
}

// checkBuckets sorts CI checks into the groups the Checks tab shows: failed
// first, then running, passed and the rest (skipped, cancelled, unknown).
export interface CheckBuckets<T> {
  failed: T[]
  running: T[]
  passed: T[]
  other: T[]
}
export function checkBuckets<T extends Partial<CheckSummary>>(checks: readonly T[]): CheckBuckets<T> {
  const out: CheckBuckets<T> = { failed: [], running: [], passed: [], other: [] }
  for (const c of checks) {
    const { tone } = checkMeta(c)
    out[tone === 'coral' ? 'failed' : tone === 'run' ? 'running' : tone === 'sage' ? 'passed' : 'other'].push(c)
  }
  return out
}

// checkSummary is the one rule for how a change's checks stand, for the
// conversation's CI line and the merge box: failed, running, incomplete (a
// check was cancelled, needs action or is unknown), passed (skipped and
// neutral checks do not block, as on GitHub), or none. It keeps the lists.
export function checkSummary<T extends Partial<CheckSummary>>(checks?: readonly T[] | null) {
  const all = checks ?? []
  const { failed, running, passed, other } = checkBuckets(all)
  const skipped = other.filter((c) => ['skipped', 'neutral'].includes(checkMeta(c).word))
  const open = other.filter((c) => !skipped.includes(c))
  const state: 'none' | 'failed' | 'running' | 'incomplete' | 'passed' = !all.length ? 'none' : failed.length ? 'failed' : running.length ? 'running' : open.length ? 'incomplete' : 'passed'
  return { state, total: all.length, failed, running, passed, skipped, open }
}

// workflowGroups splits checks named "workflow / job" by workflow, keeping
// the order of first appearance. A check with no workflow joins "Checks".
export function workflowGroups<T extends Pick<CheckSummary, 'name'>>(checks: readonly T[]): { workflow: string; items: (T & { job: string })[] }[] {
  const groups = new Map<string, (T & { job: string })[]>()
  for (const c of checks) {
    const at = c.name.indexOf(' / ')
    const workflow = at > 0 ? c.name.slice(0, at) : 'Checks'
    const items = groups.get(workflow) ?? []
    items.push({ ...c, job: at > 0 ? c.name.slice(at + 3) : c.name })
    groups.set(workflow, items)
  }
  return [...groups].map(([workflow, items]) => ({ workflow, items }))
}

// The watcher's own instructions to agents; a person reading the feed does
// not need them.
const AGENT_HINTS = [/\s*Mark every GitHub comment with <!--[^>]*-->\.?/g, /\s*Inspect dyna git pr show \S+\.?/g, /<!--[^>]*-->/g]

export interface PREvent {
  kind: string
  label: string
  tone: string
  detail?: string
}

export interface ParsedPost {
  url: string
  number: number
  repo: string
  head: string
  events: PREvent[]
  body: string
  log: string
  dropped: number
}

const EVENTS: { re: RegExp; make: (m: RegExpMatchArray) => PREvent }[] = [
  { re: /^CI failed \((\d)\/3\): (.*?)\. Fix the failure/, make: (m) => ({ kind: 'ci-failed', label: `CI failed ${m[1]}/3`, tone: 'coral', detail: m[2] }) },
  { re: /failed CI three times in a row/, make: () => ({ kind: 'ci-paused', label: 'CI wakes paused after 3 failures', tone: 'gold' }) },
  { re: /^Reviewer requests changes/, make: () => ({ kind: 'changes', label: 'Changes requested', tone: 'gold' }) },
  { re: /approved by its Dyna reviewer/, make: () => ({ kind: 'approved', label: 'Approved by the reviewer', tone: 'sage' }) },
  { re: /^GitHub (\S+) from (\S+?)(?: \(comment [^)]*\))?:/, make: (m) => ({ kind: 'comment', label: `${m[1] === 'review' ? 'Review' : 'Comment'} from ${m[2]}`, tone: 'quiet' }) },
  { re: /^PR merged\./, make: () => ({ kind: 'merged', label: 'Merged', tone: 'sage' }) },
  { re: /^PR closed\./, make: () => ({ kind: 'closed', label: 'Closed', tone: 'faint' }) },
  { re: /^Review PR /, make: () => ({ kind: 'review', label: 'Review assigned', tone: 'quiet' }) },
]

// parsePRPost reads a watcher post: "<url>\nHead: <sha>\n\n<notices>", the
// notices joined by blank lines, an optional "Log tail:" block at the end.
// It returns {url, number, repo, head, events, body, log}.
//
// TODO: legacy/shim remove later — the watcher posts typed pr/* events now and
// PRCard reads their payload. This text reader serves only the posts stored
// before that change; remove it when that history ages out.
export function parsePRPost(text: string | null | undefined, notice: EventNotice | null = null): ParsedPost {
  let rest = text || ''
  const out: ParsedPost = { url: '', number: 0, repo: '', head: '', events: [], body: '', log: '', dropped: 0 }
  const first = rest.match(/^(https?:\/\/\S+)\n/)
  if (first) {
    out.url = first[1] ?? ''
    rest = rest.slice(first[0].length)
    const m = out.url.match(/github\.com\/([^/]+\/[^/]+)\/pull\/(\d+)/)
    if (m) { out.repo = m[1] ?? ''; out.number = Number(m[2]) }
  }
  const head = rest.match(/^Head: (\S+)\n?/)
  if (head) { out.head = head[1] ?? ''; rest = rest.slice(head[0].length) }
  const cut = rest.indexOf('\n\nLog tail:\n')
  if (cut >= 0) { out.log = rest.slice(cut + 12).trimEnd(); rest = rest.slice(0, cut) }
  rest = rest.trim()
  // "Comment filter: dropped N comments (...)" counts the noise the watcher
  // dropped; it reads as a quiet note, not body text.
  let dropped = 0
  rest = rest.replace(/(^|\n\n)Comment filter: dropped (\d+) comments?[^\n]*(?=\n\n|$)/g, (_, lead: string, n: string) => {
    dropped += Number(n)
    return lead
  }).trim()
  // TODO: legacy/shim remove later — a post stored before the typed PR events
  // carries its ready row in text only; remove when that history ages out.
  if (notice?.kind === 'ready') {
    out.events.push({ kind: 'ready', label: 'Ready to land', tone: 'sage' })
    rest = rest.replace(/^PR #\d+ is ready to land at \S+?\.(?:\n+|$)/m, '').trim()
  }
  // A verdict notice carries the reviewer's markdown. Its paragraphs are
  // body, not notices, until the next notice header.
  let inVerdictBody = false
  for (const para of rest.split(/\n{2,}/)) {
    if (!inVerdictBody) {
      for (const e of EVENTS) {
        const m = para.match(e.re)
        if (m) { out.events.push(e.make(m)); break }
      }
    }
    if (/^[^\n]*? at \S+ approved by its Dyna reviewer:|^Reviewer (?:requests changes|has a question) for /.test(para)) inVerdictBody = true
    else if (/^(?:GitHub \S+ from \S+|PR merged\.|PR closed\.|Review PR |CI failed \(\d\/3\))/.test(para)) inVerdictBody = false
  }
  // Several comments in one batch read as one count.
  const comments = out.events.filter((e) => e.kind === 'comment')
  if (comments.length > 1) {
    out.events = [...out.events.filter((e) => e.kind !== 'comment'), { kind: 'comment', label: `${comments.length} new comments`, tone: 'quiet' }]
  }
  if (!out.events.length) out.events.push({ kind: 'update', label: 'Update', tone: 'quiet' })
  if (dropped) out.events.push({ kind: 'noise', label: `${dropped} dropped as noise`, tone: 'faint' })
  out.dropped = dropped
  let body = rest
  for (const re of AGENT_HINTS) body = body.replace(re, '')
  // One comment: the row already names its author, so drop the header line.
  if (comments.length === 1) body = body.replace(/^GitHub \S+ from \S+?(?: \(comment [^)]*\))?:\n/m, '')
  // The row names the verdict and the head; the lead-in only repeats them.
  body = body.replace(/^[^\n]{0,160}? at \S+ approved by its Dyna reviewer:\s*/m, '')
  out.body = body.trim()
  return out
}

export const shortSha = (s?: string | null): string => (s || '').slice(0, 7)

// commitUrl is the GitHub page of a commit whose pull request URL is known. A
// change without a pull request link has no commit link either.
export const commitUrl = (url?: string | null, sha?: string | null): string =>
  url && sha ? url.replace(/\/pull\/\d+.*$/, `/commit/${sha}`) : ''

// verdictMeta says how a verdict reads. approve-with-nits is an approval
// with a note; question asks for an answer, not for code.
export function verdictMeta(verdict?: string | null): { label: string; tone: string; note: string } | null {
  switch (verdict) {
    case 'approve': return { label: 'Approved', tone: 'ok', note: '' }
    case 'approve-with-nits': return { label: 'Approved', tone: 'ok', note: 'With nits. Fix them; no other round is needed.' }
    case 'question': return { label: 'Question', tone: 'warn', note: 'The reviewer needs an answer, not code.' }
    case 'changes': return { label: 'Changes requested', tone: 'warn', note: '' }
    default: return null
  }
}

// A local change has a branch and a base but no PR number or link.
export const isLocal = (c?: Facts | null): boolean => c?.kind === 'local'

// A base can be a commit; show it short.
export const refName = (r?: string | null): string => (/^[0-9a-f]{40}$/.test(r || '') ? shortSha(r) : r || '')

// changeName is the short name of a change: "#12" or "branch → base".
export const changeName = (c?: Facts | null): string => {
  if (!isLocal(c) && c?.number) return `#${c.number}`
  const base = refName(c?.baseRef || c?.base)
  return `${c?.branch || 'branch'}${base ? ` → ${base}` : ''}`
}

// changeTitle is the heading of a change.
export const changeTitle = (c?: Facts | null): string => c?.title || (isLocal(c) || !c?.number ? c?.branch || 'Local change' : `${c.repo} #${c.number}`)

export const SEVERITIES: { id: string; label: string; tone: string }[] = [
  { id: 'owner', label: 'Your comments', tone: 'iris' },
  { id: 'blocking', label: 'Blocking', tone: 'coral' },
  { id: 'should-fix', label: 'Should fix', tone: 'gold' },
  { id: 'nit', label: 'Nit', tone: 'faint' },
]

// groupFindings splits findings by severity, heaviest first. Empty groups
// are left out, and an unknown severity reads as a nit.
export function groupFindings<F extends Pick<Finding, 'severity'>>(findings?: readonly F[] | null): { id: string; label: string; tone: string; items: F[] }[] {
  const known = new Set(SEVERITIES.map((s) => s.id))
  return SEVERITIES.map((s) => ({ ...s, items: (findings ?? []).filter((f) => (known.has(f.severity) ? f.severity : 'nit') === s.id) })).filter((g) => g.items.length)
}

const DISPOSITIONS: { id: string; label: string; tone: string }[] = [
  { id: 'fixed', label: 'Fixed', tone: 'sage' },
  { id: 'wont-fix', label: "Won't fix", tone: 'gold' },
  { id: 'question', label: 'Question', tone: 'quiet' },
  { id: 'resolved', label: 'Resolved', tone: 'sage' },
]
export const dispositionMeta = (id: string) => DISPOSITIONS.find((d) => d.id === id) ?? { id, label: id || 'Reply', tone: 'quiet' }

// Room change policy: how checks, review and landing work. Each list gives
// the wire value and the words the owner reads.
export const POLICY_REVIEW = [{ id: 'on', label: 'On' }, { id: 'off', label: 'Off' }]

// BudgetForm is the CI budget part of a policy form. The lists are text, one
// entry per line.
export interface BudgetForm {
  ciTimeBudget: string
  ciBudgetExempt: string
  ciWarnAfter: string
  ciFailAfter: string
  ciTestMax: string
  ciTestAllow: string
  ciTestAllowNotes: Record<string, string>
  ciNoJUnitWarning: boolean
}

// The form of the room policy. Every field is text the owner edits.
export interface PolicyForm extends Partial<BudgetForm> {
  requiredChecks: string
  review?: string
}

// The settings a change can override.
export interface OverrideForm {
  requiredChecks: string
  review: string
}

// A policy with every field the host may leave out filled in.
export interface NormalizedPolicy extends ChangePolicy {
  requiredChecks: string[]
}

export const DEFAULT_CI_BUDGET = { ciTimeBudget: 'warn', ciBudgetExempt: [], ciWarnAfter: '2m', ciFailAfter: '3m', ciTestMax: '10s', ciTestAllow: [], ciNoJUnitWarning: true }
// Legacy room policies can omit budget fields. An explicit false stays false.
export const budgetForm = (p?: Partial<ChangePolicy> | null) => {
  const notes: Record<string, string> = Object.create(null)
  return {
    ...DEFAULT_CI_BUDGET, ...p,
    ciTimeBudget: p?.ciTimeBudget || 'warn',
    ciWarnAfter: p?.ciWarnAfter || DEFAULT_CI_BUDGET.ciWarnAfter,
    ciFailAfter: p?.ciFailAfter || DEFAULT_CI_BUDGET.ciFailAfter,
    ciTestMax: p?.ciTestMax || DEFAULT_CI_BUDGET.ciTestMax,
    ciBudgetExempt: (p?.ciBudgetExempt ?? []).join('\n'),
    ciTestAllow: (p?.ciTestAllow ?? []).join('\n'),
    ciTestAllowNotes: Object.assign(notes, p?.ciTestAllowNotes),
    ciNoJUnitWarning: p?.ciNoJUnitWarning ?? DEFAULT_CI_BUDGET.ciNoJUnitWarning,
  }
}
// Only outer whitespace is removed. Commas, #, and internal spaces stay exact.
export const parseTestAllow = (text?: string | null): string[] => [...new Set(String(text ?? '').split(/\r?\n/).map((s) => s.trim()).filter(Boolean))]
export const DEFAULT_POLICY: NormalizedPolicy = { requiredChecks: [], review: 'on' }

const labelOf = (list: { id: string; label: string }[], id?: string): string => list.find((o) => o.id === id)?.label ?? id ?? ''
export const policyLabel = {
  review: (id?: string): string => labelOf(POLICY_REVIEW, id),
}

// parseChecks turns "a, b" into ["a","b"]; it drops blanks and repeats.
export const parseChecks = (text?: string | null): string[] => [...new Set(String(text ?? '').split(/[,\n]/).map((s) => s.trim()).filter(Boolean))]
export const checksText = (list?: readonly string[] | null): string => (list ?? []).join(', ')

// normalizePolicy fills any field the host left out with the default.
export const normalizePolicy = (p?: Partial<ChangePolicy> | null): NormalizedPolicy => {
  const next: NormalizedPolicy = { ...DEFAULT_POLICY, ...(p ?? {}), requiredChecks: p?.requiredChecks ?? DEFAULT_POLICY.requiredChecks }
  // TODO: legacy/shim remove later — discard retired policy fields from older hosts; remove when all hosts send current policies.
  Reflect.deleteProperty(next, 'checks')
  Reflect.deleteProperty(next, 'land')
  // TODO: legacy/shim remove later — discard local check aliases from older hosts; remove when all hosts send current policies.
  next.requiredChecks = next.requiredChecks.filter((name) => !['go-test-touched', 'frontend-if-touched'].includes(name))
  return next
}

// overrideKeys lists the settings a policyOverride changes.
const OVERRIDE_FIELDS = ['requiredChecks', 'review'] as const
export type OverrideField = (typeof OVERRIDE_FIELDS)[number]
// An array is present even when empty: clearing required checks is an override.
export const overrideKeys = (o?: Partial<PolicyOverride> | null): OverrideField[] => OVERRIDE_FIELDS.filter((k) => (Array.isArray(o?.[k]) ? true : String(o?.[k] ?? '').trim()))

// buildOverride compares the form with the room policy. Only settings that
// differ go in the override. An empty override (just the reason) resets it.
export function buildOverride(room: Partial<ChangePolicy> | null | undefined, form: OverrideForm, reason?: string | null): Partial<PolicyOverride> & { reason: string } {
  const base = normalizePolicy(room)
  const out: Partial<PolicyOverride> & { reason: string } = { reason: String(reason ?? '').trim() }
  for (const k of ['review'] as const) if (form[k] !== base[k]) out[k] = form[k]
  const req = parseChecks(form.requiredChecks)
  if (req.join('\n') !== base.requiredChecks.join('\n')) out.requiredChecks = req
  return out
}

// Save helpers accept the bridge so tests can use a mock.
export interface PolicyBridge {
  setRoomChangePolicy(roomId: string, policy: NormalizedPolicy): Promise<unknown>
  setChangePolicyOverride(roomId: string, changeId: string, override: Partial<PolicyOverride>): Promise<unknown>
}

// saveRoomPolicy sends the whole room policy and returns what it sent.
export async function saveRoomPolicy(bridge: Pick<PolicyBridge, 'setRoomChangePolicy'>, roomId: string, form: PolicyForm, original: Partial<ChangePolicy> | null = null): Promise<NormalizedPolicy> {
  const { ciBudgetExempt, ciTestAllow, ciTestAllowNotes, ...rest } = form
  const next = normalizePolicy({ ...rest, requiredChecks: parseChecks(form.requiredChecks), ...(ciTestAllowNotes ? { ciTestAllowNotes } : {}) })
  if (typeof ciBudgetExempt === 'string') next.ciBudgetExempt = parseTestAllow(ciBudgetExempt)
  if (typeof ciTestAllow === 'string') {
    next.ciTestAllow = parseTestAllow(ciTestAllow)
    const previous = new Set(original?.ciTestAllow ?? [])
    const notes = next.ciTestAllow
      .filter((id) => String(ciTestAllowNotes?.[id] ?? '').trim())
      .map((id): [string, string] => [id, String(ciTestAllowNotes?.[id]).trim()])
    next.ciTestAllowNotes = Object.fromEntries(notes)
    if (original) {
      for (const id of next.ciTestAllow) {
        if (!previous.has(id) && !next.ciTestAllowNotes[id]) throw new Error(`Add a measured time and reason for ${id}.`)
      }
    }
  }
  await bridge.setRoomChangePolicy(roomId, next)
  return next
}

// saveOverride sends a change's override. A differing setting needs a reason;
// the host call is skipped without one. No difference resets the override.
export async function saveOverride(bridge: Pick<PolicyBridge, 'setChangePolicyOverride'>, roomId: string, changeId: string, room: Partial<ChangePolicy> | null | undefined, form: OverrideForm, reason?: string | null): Promise<void> {
  const draft = buildOverride(room, form, reason)
  if (overrideKeys(draft).length && !draft.reason) throw new Error('Say why this change needs different settings.')
  await bridge.setChangePolicyOverride(roomId, changeId, overrideKeys(draft).length ? draft : { reason: '' })
}
export const resetOverride = (bridge: Pick<PolicyBridge, 'setChangePolicyOverride'>, roomId: string, changeId: string): Promise<unknown> => bridge.setChangePolicyOverride(roomId, changeId, { reason: '' })
