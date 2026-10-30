// The Services page's tabs: one per service family, each with a one-line state
// and the results that service produced. A trial groups its tests and its
// publish, repeated passing hook runs fold into one row per day, and a failure
// always keeps its own row. Nothing here changes data.
import { elapsed, nextAt, queueCounts, queueLine, testLine, until, type HookRow, type HookRunRow, type Inputs, type MergeHistoryRow, type Row, type RunReason, type Tone } from './model.ts'

export type TabId = 'merges' | 'reviews' | 'updates' | 'librarian' | 'ci' | 'more'

// Step is one outcome inside an entry, such as a trial's pass and its publish.
export interface Step {
  text: string
  tone: Tone
}

// Entry is one thing a service did: a review, a trial with its publish, a hook
// run, or a folded day of passing runs. run names the hook run behind an entry
// that opens its log.
export interface Entry {
  key: string
  at: number
  text: string
  tone: Tone
  session: string
  run?: { hook: string; id: string }
  steps: Step[]
}

export interface Tab {
  id: TabId
  name: string
  tone: Tone
  // line is the one-line state under the tab strip: running, idle, failing or
  // when it runs next.
  line: string
  // rows are the service lines whose controls sit in the tab's header.
  rows: Row[]
  entries: Entry[]
}

const rowIds: Record<Exclude<TabId, 'more'>, string> = { merges: 'merge', reviews: 'reviewer', updates: 'update', librarian: 'librarian', ci: 'ci' }
const moreIds = ['radar', 'watcher', 'lanesync', 'maintenance', 'usage', 'voice']

const isFailure = (result: string): boolean => result === 'error' || result === 'fail'
const resultWord = (result: string): string => (result === 'pass' ? 'passed' : isFailure(result) ? 'failed' : result === 'pending' ? 'running' : result || 'unknown')
const plural = (n: number, word: string): string => `${n} ${word}${n === 1 ? '' : 's'}`

// mergeEntries groups the trial row and the publish row of one trial branch
// into one entry. A row with no branch stays its own entry.
export function mergeEntries(history: MergeHistoryRow[]): Entry[] {
  const groups = new Map<string, MergeHistoryRow[]>()
  history.forEach((h, n) => {
    const id = h.kind !== 'decision' && h.branch ? h.branch : `row:${n}`
    groups.set(id, [...(groups.get(id) ?? []), h])
  })
  const out: Entry[] = []
  for (const [id, group] of groups) {
    const chronological = [...group].sort((a, b) => Date.parse(a.at) - Date.parse(b.at))
    const newest = chronological[chronological.length - 1]
    const first = chronological[0]
    if (!newest || !first) continue
    if (first.kind === 'decision') {
      out.push({ key: `merge:${id}:${first.at}`, at: Date.parse(first.at), text: `Decision${first.detail ? `: ${first.detail}` : ''}`, tone: 'on', session: '', steps: [] })
      continue
    }
    const number = id.match(/merge-trial\/(\d+)$/)?.[1]
    const labels = group.find((h) => h.labels.length)?.labels ?? []
    const changes = labels.length ? labels.join(', ') : plural(first.changes, 'change')
    // A trial that passed and published reads as one step; a failure keeps its reason.
    const publish = chronological.find((h) => h.kind === 'publish')
    const trial = chronological.find((h) => h.kind === 'trial')
    const published = publish ? `published main · ${publish.head.slice(0, 7) || plural(publish.changes, 'change')}` : ''
    const steps: Step[] = trial && trial.outcome !== 'passed'
      ? [{ text: `failed${trial.detail ? `: ${trial.detail}` : ''}`, tone: 'error' }]
      : [{ text: ['passed', published].filter((t) => t && !(t === 'passed' && !trial)).join(' · '), tone: 'on' }]
    out.push({
      key: `merge:${id}:${newest.at}`, at: Date.parse(newest.at),
      text: `${number ? `Trial ${number}` : 'Trial'} · ${changes}`,
      tone: steps.some((s) => s.tone === 'error') ? 'error' : 'on', session: '', steps,
    })
  }
  return out.sort((a, b) => b.at - a.at)
}

// runReason names why a failed run failed: its exit code, or the log's last
// line when the log records no exit code.
function runReason(reason: RunReason | undefined): string {
  if (!reason) return ''
  return reason.exitCode >= 0 ? `exit ${reason.exitCode}` : reason.detail
}

// hookEntries lists a hook's runs. Passing runs of one day fold into one row;
// a failed or running run keeps its own row, and a failed row names its reason
// and opens the run's log.
export function hookEntries(row: HookRow, runs: HookRunRow[], now: number, reasons: Record<string, RunReason>, label = row.hook.label || row.hook.name): Entry[] {
  const { hook } = row
  const list = runs.length ? runs : row.lastRun ? [row.lastRun] : []
  const today = new Date(now).toDateString()
  const out: Entry[] = []
  const passes = new Map<string, HookRunRow[]>()
  for (const run of list) {
    const at = Date.parse(run.startedAt)
    if (!Number.isFinite(at) || at <= 0) continue
    if (run.result === 'pass') {
      const day = new Date(at).toDateString()
      passes.set(day, [...(passes.get(day) ?? []), run])
      continue
    }
    const failed = isFailure(run.result)
    const why = failed ? runReason(reasons[run.id]) : ''
    // A failure that ended at once shows no duration; its reason carries the row.
    const took = run.result === 'pending' ? '' : elapsed(run.startedAt, run.finishedAt)
    out.push({
      key: `hook:${hook.name}:${run.startedAt}`, at,
      text: `${label} · ${resultWord(run.result)}${why ? `: ${why}` : ''}${took && took !== '0 s' ? ` in ${took}` : ''}`,
      tone: run.result === 'pending' ? 'live' : failed ? 'error' : 'off', session: '',
      ...(failed && run.id ? { run: { hook: hook.name, id: run.id } } : {}),
      steps: [],
    })
  }
  for (const [day, group] of passes) {
    const newest = group.reduce((a, b) => (Date.parse(b.startedAt) > Date.parse(a.startedAt) ? b : a))
    const at = Date.parse(newest.startedAt)
    const took = elapsed(newest.startedAt, newest.finishedAt)
    out.push({
      key: `hook:${hook.name}:${day}`, at, tone: 'on', session: '', steps: [],
      text: group.length > 1 ? `${label} · ${group.length} runs passed${day === today ? ' today' : ''}` : `${label} · passed${took ? ` in ${took}` : ''}`,
    })
  }
  return out
}

function reviewEntries(i: Inputs, now: number): Entry[] {
  const out: Entry[] = []
  if (i.reviewer?.current) {
    out.push({ key: 'review:now', at: now, text: `Reviewing #${i.reviewer.current.pr} · ${i.reviewer.current.title}`, tone: 'live', session: '', steps: [] })
  }
  i.changes.forEach((c, n) => {
    if (!c.reviewAt) return
    const verdict = c.verdict ? c.verdict.replaceAll('-', ' ') : 'reviewed'
    const name = c.number ? `#${c.number}` : c.title || c.key || 'local change'
    out.push({
      key: `verdict:${c.key ?? c.number ?? n}:${c.reviewAt}`, at: Date.parse(c.reviewAt),
      text: `${verdict} ${name}${c.number && c.title ? ` · ${c.title}` : ''}`,
      tone: c.verdict === 'changes' ? 'error' : 'on', session: c.reviewerSession ?? '', steps: [],
    })
  })
  return out
}

const byNewest = (list: Entry[]): Entry[] => list.filter((e) => Number.isFinite(e.at) && e.at > 0).sort((a, b) => b.at - a.at)

function hookTone(rows: HookRow[], runs: Inputs['runs']): Tone {
  const newest = (r: HookRow): string => runs[r.hook.name]?.[0]?.result ?? r.lastRun?.result ?? ''
  if (rows.some((r) => isFailure(newest(r)))) return 'error'
  return rows.some((r) => newest(r) === 'pending') ? 'live' : 'on'
}

export function buildTabs(i: Inputs, rows: Row[], now: number): Tab[] {
  const row = (id: string): Row | undefined => rows.find((r) => r.id === id)
  const pick = (id: Exclude<TabId, 'more'>): Row[] => rows.filter((r) => r.id === rowIds[id])
  const hooks = i.hooks ?? []
  const libraryHooks = hooks.filter((h) => h.hook.name.startsWith('librarian-'))
  const otherHooks = hooks.filter((h) => !h.hook.name.startsWith('librarian-'))
  // Hooks that share a label show their own names, so their rows differ.
  const labels = hooks.map((h) => h.hook.label || h.hook.name)
  const labelOf = (h: HookRow): string => (labels.filter((l) => l === (h.hook.label || h.hook.name)).length > 1 ? h.hook.name : h.hook.label || h.hook.name)
  const entriesOf = (list: HookRow[]): Entry[] => byNewest(list.flatMap((h) => hookEntries(h, i.runs[h.hook.name] ?? [], now, i.reasons, labelOf(h))))

  const merges = mergeEntries(i.history)
  const merge = row('merge')
  const lastFailed = merges.find((e) => e.steps.length)?.tone === 'error'
  const mergeLine = [merge?.state === 'paused' ? 'Paused' : lastFailed ? 'Last trial failed' : '', queueLine(i.queue)].filter(Boolean).join(' · ')

  const rev = row('reviewer')
  const reviewLine = !rev ? '' : rev.state === 'error' || rev.state === 'off' || rev.state === 'unreadable'
    ? `${rev.state === 'error' ? 'Failing' : rev.state === 'off' ? 'Off' : 'Unreadable'} · ${rev.detail}`
    : rev.state === 'rate-limited' ? `Rate-limited · ${rev.detail}; reviews resume on their own`
      : rev.state === 'idle' ? 'Idle · nothing to review now' : `${rev.state === 'running' ? 'Running' : 'Waiting'} · ${rev.detail}`

  const update = hooks.find((h) => h.hook.name === 'self-update') ?? null
  const updateNext = nextAt(update)
  const updateRow = row('update')
  const updateLine = updateRow ? [updateRow.detail, updateNext ? `next ${until(updateNext, now)}` : ''].filter(Boolean).join(' · ') : ''

  const librarian = row('librarian')
  const nextText = librarian?.nextAt ? `next ${until(librarian.nextAt, now)}` : ''
  const librarianLine = !librarian ? '' : librarian.state === 'on'
    ? ['On', nextText].filter(Boolean).join(' · ')
    : `${librarian.state === 'off' ? 'Off' : 'Unknown'} · ${librarian.detail}`

  const slow = testLine(i.testHealth)
  const counts = queueCounts(i.queue)

  const tabs: Tab[] = [
    { id: 'merges', name: 'Merges', tone: lastFailed ? 'error' : counts.running ? 'live' : merge?.tone ?? 'off', line: mergeLine, rows: pick('merges'), entries: merges },
    { id: 'reviews', name: 'Reviews', tone: rev?.tone ?? 'off', line: reviewLine, rows: pick('reviews'), entries: byNewest(reviewEntries(i, now)) },
    { id: 'updates', name: 'Updates and hooks', tone: updateRow?.tone === 'error' ? 'error' : hookTone(otherHooks, i.runs) === 'error' ? 'error' : updateRow?.tone ?? 'off', line: updateLine, rows: pick('updates'), entries: entriesOf(otherHooks) },
    { id: 'librarian', name: 'Librarian', tone: librarian?.tone ?? 'off', line: librarianLine, rows: pick('librarian'), entries: entriesOf(libraryHooks) },
    { id: 'ci', name: 'CI health', tone: slow ? 'live' : 'on', line: slow || 'No test is over the limit', rows: pick('ci'), entries: [] },
    { id: 'more', name: 'More', tone: 'off', line: '', rows: moreIds.map(row).filter((r): r is Row => !!r), entries: [] },
  ]
  return tabs
}
