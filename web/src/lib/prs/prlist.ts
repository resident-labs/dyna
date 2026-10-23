// What the Pull requests screen shows: the list groups, one chip per row, and
// the detail's status box. The functions are pure; GitHub.svelte and
// PRDetail.svelte draw the result.
import type { BotReview, ChangePolicy, Finding, MainCI, MergeQueueItem, MergeTrialStatus } from '../api/change.ts'
import type { GitHubItem, GitHubUser } from '../api/github.ts'
import { ordinal, verdictMeta, isLocal, prTracks, withCI, checkSummary, checkMeta, type Facts, type Meta } from './pr.ts'

// A list row: a change, or the stand-in row of a GitHub PR no change covers.
// A detail row also holds its findings.
export interface Row extends Facts {
  policy?: ChangePolicy
  synthetic?: boolean
  author?: string
  isBot?: boolean
  findings?: Finding[] | null
}
// The merge item fields these helpers read; the engine sends the whole MergeQueueItem.
export interface MergeItem extends Pick<MergeQueueItem, 'state'>, Partial<Omit<MergeQueueItem, 'state' | 'trial' | 'main'>> {
  trial?: Pick<MergeTrialStatus, 'phase'> & Partial<MergeTrialStatus>
  main?: Pick<MainCI, 'state'> & Partial<MainCI>
}
// The GitHub item fields a stand-in row reads.
export type GitHubRow = Pick<GitHubItem, 'number'> & Partial<GitHubItem>
type Lane = 'ci' | 'review' | 'fix' | 'landing' | 'done'
// Session ids mapped to their names.
export type Names = Record<string, string>

export const isDone = (c?: Row | null): boolean => c?.state === 'merged' || c?.state === 'closed'
export const isUntracked = (c?: Row | null): boolean => !isDone(c) && (c?.status === 'untracked' || !!c?.synthetic)
const hasCI = (c: Row): boolean => c?.ci === 'passed'

// isReady says the change can land: the reviewer
// approved its current head and GitHub CI passed.
export const isReady = (c?: Row | null): boolean => !isDone(c) && !isUntracked(c) && prTracks(c).ready

// laneOf is the review lane of a row the merge service does not list, such
// as a local change: ci, fix, review, landing or done.
export function laneOf(c: Row): Lane | null {
  if (isDone(c)) return 'done'
  if (isUntracked(c)) return null
  if (prTracks(c).held) return ['running', 'failed', 'unknown'].includes(prTracks(c).ci) ? 'ci' : 'review'
  if (prTracks(c).ci === 'failed' || asksChanges(c)) return 'fix'
  if (['queued', 'reviewing', 'blocked'].includes(c.reviewState ?? '') || c.verdict === 'question') return 'review'
  if (prTracks(c).ready) return 'landing'
  return ['passed', 'absent', 'local'].includes(prTracks(c).ci) ? 'review' : 'ci'
}

// classify gives a row's review lane and status chip without a merge item.
export function classify(c: Row): { group: string; label: string; tone: string } {
  return { group: laneOf(c) ?? 'untracked', ...statusChip(c) }
}

function statusChip(c: Row): Meta {
  if (c?.state === 'merged') return { label: 'Merged', tone: 'done' }
  if (c?.state === 'closed') return { label: 'Closed', tone: 'faint' }
  if (isUntracked(c)) return { label: 'Not tracked', tone: 'faint' }
  if (prTracks(c).held) return { label: 'Held', tone: 'faint' }
  if (c.ciPaused || c.status === 'paused' || c.pauseReason) {
    const review = !c.ciPaused && /reviewer/i.test(c.pauseReason || '')
    return { label: review ? 'Review paused' : 'Paused', tone: 'gold' }
  }
  if (asksChanges(c)) return { label: 'Changes asked', tone: 'coral' }
  if (c.verdict === 'question') return { label: 'Question', tone: 'gold' }
  if (c.ci === 'failed' || c.status === 'ci-failed') {
    const n = c.consecutiveFailures || 0
    return { label: n ? `CI failed ${Math.min(n, 3)}/3` : 'CI failed', tone: 'coral' }
  }
  if (isReady(c)) return { label: 'Ready to land', tone: 'sage' }
  if (c.reviewState === 'blocked') return { label: 'Review blocked', tone: 'gold' }
  if (prTracks(c).approved && prTracks(c).missing.length) return { label: 'Needs required reviewers', tone: 'gold' }
  if (prTracks(c).approved) return { label: withCI('Approved', c), tone: 'run' }
  if (c.reviewState === 'reviewing') return { label: withCI('Reviewing', c), tone: 'iris' }
  if (c.reviewState === 'queued') return { label: withCI(c.queuePosition ? `Queued (${ordinal(c.queuePosition)})` : 'Queued', c), tone: 'quiet' }
  if (c.reviewState === 'off') return { label: withCI('Review off', c), tone: 'faint' }
  if (prTracks(c).ci === 'running') return { label: 'CI running', tone: 'gold' }
  return { label: 'In progress', tone: 'quiet' }
}

// The merge service item's state, as a list lane and a chip. The queue lane
// holds PRs that wait for their batch trial or publication.
const MERGE: Record<string, [string, string, string]> = {
  ready: ['queue', 'Ready', 'sage'],
  'trial-building': ['queue', 'Building batch', 'gold'],
  'trial-running': ['queue', 'Testing batch', 'gold'],
  'trial-publishing': ['queue', 'Merging tested batch', 'sage'],
  'trial-published': ['queue', 'Confirming merge', 'sage'],
  'trial-failed': ['queue', 'Batch CI failed', 'coral'],
  'trial-reviewing': ['queue', 'Reviewing batch', 'iris'],
  'trial-stacking': ['queue', 'Testing prefixes', 'gold'],
  returned: ['queue', 'Returned', 'coral'],
  'trial-rejected': ['blocked', 'Batch CI failed', 'coral'],
  'trial-unavailable': ['blocked', 'Trial CI needed', 'gold'],
  'waiting-repository': ['progress', 'Waiting for repository', 'quiet'],
  'read-only': ['blocked', 'Read-only room', 'faint'],
  held: ['blocked', 'Draft', 'faint'],
  conflict: ['blocked', 'Conflict', 'coral'],
  'ci-failed': ['blocked', 'CI failed', 'coral'],
  changes: ['blocked', 'Changes requested', 'coral'],
  paused: ['blocked', 'Paused', 'gold'],
  'waiting-base': ['progress', 'Waiting for the base PR', 'gold'],
  review: ['progress', 'Needs review', 'quiet'],
  ci: ['progress', 'Waiting for CI', 'run'],
  waiting: ['progress', 'Waiting', 'quiet'],
}
const LANE: Record<string, string> = { ci: 'progress', review: 'progress', landing: 'progress', fix: 'blocked', done: 'done', untracked: 'untracked' }

// mergeState is the one state of a row in the list, the chip and the PR
// page: { id, lane, label, tone, reason }. item is the row's RoomMergeStatus
// answer and pos its place in the merge queue (1 is next). A row the merge
// service does not list (a local change, a merged or untracked PR, or any
// row before the answer loads) keeps its review chip from classify.
export interface MergeState {
  id: string
  lane: string | undefined
  label: string
  tone: string
  reason: string
}
export function mergeState(c: Row, item: MergeItem | null = null, pos = 0): MergeState {
  const m = !isDone(c) && !isUntracked(c) && item ? MERGE[item.state] : undefined
  if (!m && !isLocal(c) && isReady(c)) return { id: 'approved', lane: 'progress', label: 'Approved', tone: 'sage', reason: 'Waiting for the merge service' }
  if (!m || !item) {
    const { group, label, tone } = classify(c)
    const id = isDone(c) ? (c.state ?? '') : isUntracked(c) ? 'untracked' : isLocal(c) ? 'local' : group
    return { id, lane: LANE[group], label, tone, reason: '' }
  }
  const [lane, label, tone] = m
  // The reason shows under the title only when it says more than the chip.
  const said = (item.reason || '').trim()
  const reason = said.toLowerCase() === label.toLowerCase() ? '' : said.charAt(0).toUpperCase() + said.slice(1)
  const state = { id: item.state, lane, label, tone, reason }
  if (item.state === 'ready') return pos === 1 ? { ...state, id: 'next', label: 'Next batch' } : { ...state, label: `Ready · ${ordinal(pos)}` }
  if (item.state === 'changes' && item.reason === 'question for the author') return { ...state, label: 'Question', tone: 'gold', reason: '' }
  if (item.state === 'review' && c.reviewState === 'reviewing') return { ...state, id: 'reviewing', label: 'Reviewing', tone: 'iris' }
  return state
}

// The list groups under the merge queue. Merged rows start folded; PRs Dyna
// does not watch fold away at the end.
export interface Group {
  id: string
  label: string
  foldable?: boolean
  collapsed?: boolean
  fold?: boolean
}
export const GROUPS: Group[] = [
  { id: 'blocked', label: 'Blocked', foldable: true },
  { id: 'progress', label: 'In progress' },
  { id: 'done', label: 'Merged today', collapsed: true },
  { id: 'untracked', label: 'Not tracked', fold: true },
]

const byRecent = (a: Row, b: Row): number => (b.number || 0) - (a.number || 0)

// listView splits rows into the merge queue, in the merge service's order,
// and the list groups, newest first; empty groups drop out. merge is the
// RoomMergeStatus answer. states maps each changeId to its mergeState.
export function listView<T extends Row>(rows: T[] | null | undefined, merge: (MergeItem & { changeId: string })[] | null | undefined): { queue: { row: T; item: MergeItem; state: MergeState }[]; groups: (Group & { rows: T[] })[]; states: Record<string, MergeState> } {
  const items = new Map((merge ?? []).map((m, i) => [m.changeId, { item: m, at: i }]))
  const states: Record<string, MergeState> = {}
  const queue: { row: T; item: MergeItem; state: MergeState }[] = []
  const out: (Group & { rows: T[] })[] = GROUPS.map((g) => ({ ...g, rows: [] }))
  const queued = (rows ?? []).flatMap((c) => {
    const entry = items.get(c.changeId ?? '')
    return entry && MERGE[entry.item.state]?.[0] === 'queue' && !isDone(c) ? [{ c, ...entry }] : []
  })
  queued.sort((a, b) => a.at - b.at)
  queued.forEach(({ c, item }, i) => {
    const state = mergeState(c, item, i + 1)
    states[c.changeId ?? ''] = state
    queue.push({ row: c, item, state })
  })
  for (const c of rows ?? []) {
    const id = c.changeId ?? ''
    if (states[id]) continue
    const state = mergeState(c, items.get(id)?.item)
    states[id] = state
    out.find((g) => g.id === state.lane)?.rows.push(c)
  }
  for (const g of out) g.rows.sort(byRecent)
  return { queue, groups: out.filter((g) => g.rows.length), states }
}

// rowsFromGitHub makes a row for each GitHub PR that no change covers. Dyna
// does not watch it, so it falls under "Not tracked".
export function rowsFromGitHub(items: GitHubRow[] | null | undefined, changes: Row[] | null | undefined, repo = ''): Row[] {
  const known = new Set((changes ?? []).filter((c) => c.number).map((c) => c.number))
  return (items ?? []).filter((it) => !known.has(it.number)).map((it) => {
    const s = (it.state || '').toUpperCase()
    return {
      synthetic: true,
      changeId: `gh:${it.number}`,
      kind: 'pr',
      repo,
      number: it.number,
      title: it.title,
      url: it.url,
      state: s === 'MERGED' || it.mergedAt ? 'merged' : s === 'CLOSED' ? 'closed' : 'open',
      status: 'untracked',
      branch: it.headRefName || '',
      base: it.baseRefName || '',
      author: it.author?.login || '',
      isBot: !!it.author?.isBot,
      isDraft: !!it.isDraft,
      checked: it.updatedAt,
    }
  })
}

// authorName is the one author every view shows: the session that wrote the
// change, or its GitHub login when no session did. All agents share the
// owner's login, so the session name is the useful one.
export function authorName(c: Row, names: Names = {}): string {
  return (c.authorSession ? names[c.authorSession] : undefined) || c.author || ''
}

// botNote is the one short line when a bot asked for changes. Bots never block.
export function botNote(c?: { botReviews?: readonly Pick<BotReview, 'bot' | 'state'>[] | null } | null): string {
  const asked = (c?.botReviews ?? []).filter((b) => b.state === 'changes_requested')
  if (!asked.length) return ''
  const names = asked.map((b) => b.bot).join(' and ')
  return `${names} asked for changes. This does not block the merge.`
}

const BOT_STATE: Record<string, string> = { changes_requested: 'asked for changes', approved: 'approved', commented: 'commented' }
export const botStateWord = (s?: string): string => BOT_STATE[s ?? ''] || s || 'commented'

// A post from a bot belongs in the Checks tab, not in the conversation.
// asksChanges is the one rule for "the reviewer asked for changes". The row chip
// and the status sentence both use it, so they cannot disagree.
export const asksChanges = (c?: Row | null): boolean => c?.verdict === 'changes' || c?.reviewState === 'changes'

// moveKey is a string that changes only when a change moves in a way its
// merge state can follow: a new change, head, verdict, review state, CI
// result, draft flag, PR state or required-reviewer lists. GitHub.svelte keys its merge reload on it.
export const moveKey = (changes: Row[] | null | undefined): string => (changes ?? []).map((c) => [c.changeId, c.head, c.verdict, c.reviewState, c.ci, !!c.isDraft, c.state, (c.requiredReviewers ?? []).join('+'), (c.missingRequiredReviewers ?? []).join('+')].join(':')).join()

// botKey normalizes a bot name the way internal/pr/watch/github.go checkBotKey
// does: lower case, no "[bot]" suffix, letters and digits only, known aliases joined.
const botAliases: Record<string, string> = { coderabbit: 'coderabbitai', copilot: 'copilot', copilotpullrequestreviewer: 'copilot', greptile: 'greptile', greptileapps: 'greptile', codex: 'codex', chatgptcodexconnector: 'codex' }
export function botKey(name?: string | null): string {
  const k = String(name ?? '').trim().toLowerCase().replace(/\[bot\]$/, '').replace(/[^\p{L}\p{N}]/gu, '')
  return botAliases[k] ?? k
}

export const isBotAuthor = (a?: Pick<GitHubUser, 'login' | 'isBot'> | null): boolean => !!a && (!!a.isBot || /\[bot\]$|^coderabbit/i.test(a.login || ''))

// stepState: ok, run, bad, warn or wait. See StatusBox for the dots.
type StepState = 'ok' | 'run' | 'bad' | 'warn' | 'wait'
export interface Step {
  name: string
  state: StepState
  note: string
}
function ciStep(c: Row): Step {
  const local = isLocal(c)
  const name = local ? 'Checks' : 'CI'
  if (c.state === 'merged' || c.ci === 'passed') return { name, state: 'ok', note: 'passed' }
  if (c.ci === 'absent') return { name, state: 'wait', note: c.ciReason || 'no GitHub CI' }
  if (c.ci === 'failed' || c.status === 'ci-failed') return { name, state: 'bad', note: `failed ${Math.min(c.consecutiveFailures || 1, 3)}/3` }
  if (local) return { name, state: 'wait', note: 'not run yet' }
  return { name, state: 'run', note: 'running' }
}

function reviewStep(c: Row): Step {
  if (c.state === 'merged') return { name: 'Review', state: 'ok', note: `round ${c.reviewRound || 1}` }
  const vm = verdictMeta(c.verdict)
  if (vm?.tone === 'ok') return { name: 'Review', state: 'ok', note: c.verdict === 'approve-with-nits' ? 'approved, nits' : 'approved' }
  if (vm?.tone === 'warn') return { name: 'Review', state: 'warn', note: c.verdict === 'question' ? 'question' : 'changes asked' }
  if (c.reviewState === 'blocked') return { name: 'Review', state: 'warn', note: 'blocked' }
  if (c.reviewState === 'reviewing') return { name: 'Review', state: 'run', note: `round ${c.reviewRound || 1}` }
  if (c.reviewState === 'queued') return { name: 'Review', state: 'wait', note: c.queuePosition ? `${ordinal(c.queuePosition)} in line` : 'queued' }
  if (c.reviewState === 'off') return { name: 'Review', state: 'wait', note: 'off' }
  return { name: 'Review', state: 'wait', note: 'not started' }
}

function mergeStep(c: Row, merge: Pick<MergeItem, 'state'> | null): Step {
  if (c.state === 'merged') return { name: 'Merge', state: 'ok', note: 'merged' }
  if (isReady(c)) return merge?.state === 'conflict' ? { name: 'Merge', state: 'bad', note: 'conflict' } : { name: 'Merge', state: 'run', note: 'ready' }
  return { name: 'Merge', state: 'wait', note: '' }
}

const MERGE_ICON: Record<string, string> = { sage: 'ok', done: 'ok', coral: 'bad', gold: 'run', run: 'run', iris: 'run' }

// mergeBox is the GitHub-style box at the end of the conversation: one row
// each for the checks, the review and the merge. status is the statusBox
// answer, for the action bar's buttons.
// state is the row's mergeState. A row is { id, icon, title, note, link };
// icon is ok, run, bad or wait, and link names the tab or part it opens.
// One row of the merge box. link names the tab it opens, or ''.
export interface BoxRow {
  id: string
  icon: string
  title: string
  note: string
  link: string
}
export function mergeBox(c: Row, { merge = null, state = null, review = true, names = {} }: { merge?: MergeItem | null; state?: MergeState | null; review?: boolean; names?: Record<string, string> } = {}): { rows: BoxRow[]; status: StatusBox } {
  const box = statusBox(c, { merge, review })
  const sum = checkSummary(c.checks)
  const checkNames = (list: { name?: string }[]) => list.slice(0, 3).map((k) => k.name).join(', ')
  const passed = `${sum.passed.length} of ${sum.total} checks passed`
  const checks = {
    none: { icon: 'wait', title: 'No checks yet', note: '' },
    failed: { icon: 'bad', title: `${sum.failed.length} ${sum.failed.length === 1 ? 'check' : 'checks'} failed`, note: checkNames(sum.failed) },
    running: { icon: 'run', title: 'Some checks are still running', note: `${passed} · ${checkNames(sum.running)} running` },
    incomplete: { icon: 'run', title: 'Some checks did not finish', note: `${passed} · ${sum.open.slice(0, 3).map((k) => `${k.name} ${checkMeta(k).word}`).join(', ')}` },
    passed: { icon: 'ok', title: 'All checks passed', note: sum.skipped.length ? `${passed} · ${sum.skipped.length} skipped` : passed },
  }[sum.state]
  const step = reviewStep(c)
  const reviewTitle: Record<'ok' | 'run' | 'bad' | 'wait', string> = { ok: 'Approved by the reviewer', run: 'The reviewer is reading this PR', bad: 'Changes requested', wait: 'Waiting for review' }
  const rv = step.state === 'warn' && step.note !== 'changes asked' ? { icon: 'run', title: step.note === 'question' ? 'The reviewer has a question' : 'The review is blocked' }
    : { icon: step.state === 'warn' ? 'bad' : step.state, title: reviewTitle[step.state === 'warn' ? 'bad' : step.state] }
  // A lean row counts its findings without loading them.
  const found = c.findingCount ?? c.findings?.length ?? 0
  // The title already says approved, so the note only adds the findings.
  const said = step.note === 'approved' ? '' : step.note
  const count = c.reviewRound && !isDone(c) ? `${found} ${found === 1 ? 'finding' : 'findings'}` : ''
  const reviewNote = [said, count].filter(Boolean).join(' · ')
  const m = state ?? mergeState(c, merge)
  const icon = MERGE_ICON[m.tone] ?? 'wait'
  const note = m.id === 'next' ? 'The merge service will test this PR in the next batch.' : m.reason || box.say
  const missing = prTracks(c).missing
  // Required reviewers show only when the PR has requirements; the names come from the room.
  const required = c.requiredReviewers?.length
    ? [{ id: 'required', icon: missing.length ? 'run' : 'ok', title: missing.length ? 'Waiting for required reviewers' : 'Required reviewers approved', note: missing.length ? `Missing: ${missing.map((id) => names[id] || id).join(', ')}` : '', link: '' }]
    : []
  return {
    rows: [
      { id: 'checks', ...checks, link: 'checks' },
      { id: 'review', ...rv, note: reviewNote, link: c.reviewRound ? 'review' : '' },
      ...required,
      { id: 'merge', icon, title: m.label, note, link: '' },
    ],
    status: box,
  }
}

// pauseText says why automation stopped, in plain words where we know the reason.
export function pauseText(r?: string | null): string {
  if (/3 consecutive failures/.test(r || '')) return 'CI failed three times in a row, so Dyna stopped waking the author. It starts again when CI passes, or press Resume.'
  return r || ''
}

// statusBox says where one change stands: three steps, one plain sentence,
// one main button and a short note. merge is the RoomMergeStatus item, review
// says whether automatic review is on in the room, and branch names the
// base for the merge sentence.
//
// Button ids: resume, track. Agents land changes after CI and review.
export interface StatusBox {
  steps: Step[]
  say: string
  button: { id: string; label: string; primary: boolean } | null
  review: boolean
  session: boolean
  note: string
}
export function statusBox(c: Row, { merge = null, review = true }: { merge?: MergeItem | null; review?: boolean } = {}): StatusBox {
  const local = isLocal(c)
  const base = c.baseRef || c.base || 'main'
  const what = local ? 'change' : 'PR'
  const steps = [ciStep(c), reviewStep(c), mergeStep(c, merge)]
  const out: StatusBox = { steps, say: '', button: null, review: false, session: false, note: botNote(c) }
  if (c.state === 'merged') return { ...out, say: `This ${what} is merged.` }
  if (c.state === 'closed') return { ...out, steps: [], say: `This ${what} is closed without a merge.` }
  if (isUntracked(c)) return { ...out, steps: [], note: '', say: `Dyna does not watch this ${what}, so it gets no automatic review.`, button: { id: 'track', label: 'Track and review', primary: true } }
  if (prTracks(c).held) return { ...out, say: 'Draft: the author must mark it ready before it can land.' }
  if (c.pauseReason || c.ciPaused || c.status === 'paused') {
    const canResume = c.ciPaused
    return { ...out, say: pauseText(c.pauseReason) || 'Automation is paused.', button: canResume ? { id: 'resume', label: 'Resume', primary: true } : null }
  }
  if (isReady(c)) {
    const conflict = merge?.state === 'conflict'
    const say = conflict
      ? `The reviewer approved this ${what}, but it conflicts with ${base}. The author must rebase it first.`
      : `The reviewer approved this ${what}. It merges cleanly into ${base}.`
    return { ...out, say: c.verdict === 'approve-with-nits' && !conflict ? `${say} The author can fix the nits later.` : say, review: !!c.hasReviewSummary }
  }
  if (c.ci === 'failed' || c.status === 'ci-failed') return { ...out, say: `CI failed. The author gets woken to fix it. You do not need to do anything yet.` }
  if (asksChanges(c)) return { ...out, say: `The reviewer asked for changes. The author fixes them, then the reviewer looks again.`, review: true }
  if (c.verdict === 'question') return { ...out, say: 'The reviewer has a question for the author.', review: true }
  if (c.reviewState === 'blocked') return { ...out, say: c.reviewBlocked || 'The reviewer cannot start this review.' }
  if (prTracks(c).missing.length) return { ...out, say: `This ${what} needs an approval from each required reviewer at its current head.`, review: !!c.hasReviewSummary }
  const ciRuns = prTracks(c).ci === 'running'
  if (prTracks(c).approved && ciRuns) return { ...out, say: `The reviewer approved this ${what}. It is ready to merge when CI passes.`, review: !!c.hasReviewSummary }
  if (c.reviewState === 'reviewing') return { ...out, say: `The reviewer is reading this ${what} now.${ciRuns ? ' CI runs at the same time.' : ''}`, session: true }
  if (c.reviewState === 'queued') return { ...out, say: `${hasCI(c) && !local ? 'CI passed. ' : ''}This ${what} is ${c.queuePosition ? ordinal(c.queuePosition) : 'waiting'} in line for review.` }
  if (!review || c.reviewState === 'off') return { ...out, say: `${hasCI(c) && !local ? 'CI passed, but automatic' : 'Automatic'} review is off, so nothing starts. Turn it on with the switch at the top.` }
  if (c.ci === 'absent') return { ...out, say: `No GitHub CI ran. Source review can continue, but merging requires passing GitHub CI.` }
  if (local) return { ...out, say: `The reviewer starts when the checks pass. You do not need to do anything yet.` }
  return { ...out, say: 'CI is running. You do not need to do anything yet.' }
}

// factsLine is the one line of facts under a change's title: who wrote it,
// the branch, the size and when it was last seen. names maps session ids to names (see authorName); byline false leaves the author out;
// size is {files, additions, deletions} from GitHub; ago turns a time into words.
export function factsLine(c: Row, { names = {}, byline = true, size = null, ago = () => '' }: { names?: Names; byline?: boolean; size?: { files?: number; additions?: number; deletions?: number } | null; ago?: (time: string) => string } = {}): string {
  const base = c.baseRef || c.base
  const branch = c.branch ? `${c.branch}${base ? ` → ${base}` : ''}` : ''
  const files = size?.files ? `${size.files} ${size.files === 1 ? 'file' : 'files'} +${size.additions || 0} −${size.deletions || 0}` : ''
  const seen = c.checked ? `updated ${ago(c.checked)}` : ''
  return [isLocal(c) ? 'Local change' : '', byline ? authorName(c, names) : '', branch, files, seen].filter(Boolean).join(' · ')
}

