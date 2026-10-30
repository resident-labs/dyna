// What a service did, for its detail: a merge trial with its publish, a
// verdict, or a hook run. Repeated passing runs of one day fold into one
// entry; a failure keeps its own entry and names its reason. Nothing here
// changes data.

// EntryTone marks an entry's dot: work running now, a failure, or a quiet result.
export type EntryTone = 'live' | 'failed' | 'done'

// Step is one outcome inside an entry, such as a trial's pass and its publish.
export interface Step {
  text: string
  tone: EntryTone
}

// Entry is one thing a service did. session opens the agent behind it; run
// opens the log of a failed hook run.
export interface Entry {
  key: string
  at: number
  text: string
  tone: EntryTone
  session: string
  run?: { hook: string; id: string }
  steps: Step[]
}

// MergeHistoryRow is one RoomMergeHistory row. branch is the trial branch a
// trial row and its publish row share; labels name the changes in it.
export interface MergeHistoryRow {
  detail: string
  kind: string
  outcome: string
  at: string
  changes: number
  branch: string
  head: string
  labels: string[]
}

// HookRunRow is one run of a hook; id names the run, so its log can open.
export interface HookRunRow {
  id: string
  result: string
  startedAt: string
  finishedAt: string
}

// RunReason is why one hook run failed, read from its log: the exit code, or
// the log's last line when it records no exit code, such as a timeout.
export interface RunReason {
  exitCode: number
  detail: string
}

// ChangeRow is the part of a room's change a verdict entry reads.
export interface ChangeRow {
  key?: string
  reviewAt?: string
  number?: number
  title?: string
  verdict?: string
  reviewerSession?: string
}

const isFailure = (result: string): boolean => result === 'error' || result === 'fail'
const resultWord = (result: string): string => (result === 'pass' ? 'passed' : isFailure(result) ? 'failed' : result === 'pending' ? 'running' : result || 'unknown')
const plural = (n: number, word: string): string => `${n} ${word}${n === 1 ? '' : 's'}`

// elapsed is a run's length, as "36 s" or "6 m 20 s".
export function elapsed(from: string, to: string): string {
  const ms = new Date(to).getTime() - new Date(from).getTime()
  if (!Number.isFinite(ms) || ms < 0) return ''
  const s = Math.round(ms / 1000)
  return s < 60 ? `${s} s` : `${Math.floor(s / 60)} m ${s % 60} s`
}

export const byNewest = (list: Entry[]): Entry[] => list.filter((e) => Number.isFinite(e.at) && e.at > 0).sort((a, b) => b.at - a.at)

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
      out.push({ key: `merge:${id}:${first.at}`, at: Date.parse(first.at), text: `Decision${first.detail ? `: ${first.detail}` : ''}`, tone: 'done', session: '', steps: [] })
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
      ? [{ text: `failed${trial.detail ? `: ${trial.detail}` : ''}`, tone: 'failed' }]
      : [{ text: ['passed', published].filter((t) => t && !(t === 'passed' && !trial)).join(' · '), tone: 'done' }]
    out.push({
      key: `merge:${id}:${newest.at}`, at: Date.parse(newest.at),
      text: `${number ? `Trial ${number}` : 'Trial'} · ${changes}`,
      tone: steps.some((s) => s.tone === 'failed') ? 'failed' : 'done', session: '', steps,
    })
  }
  return byNewest(out)
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
export function hookEntries(hook: string, label: string, runs: HookRunRow[], now: number, reasons: Record<string, RunReason>): Entry[] {
  const today = new Date(now).toDateString()
  const out: Entry[] = []
  const passes = new Map<string, HookRunRow[]>()
  for (const run of runs) {
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
      key: `hook:${hook}:${run.startedAt}`, at,
      text: `${label} · ${resultWord(run.result)}${why ? `: ${why}` : ''}${took && took !== '0 s' ? ` in ${took}` : ''}`,
      tone: run.result === 'pending' ? 'live' : failed ? 'failed' : 'done', session: '',
      ...(failed && run.id ? { run: { hook, id: run.id } } : {}),
      steps: [],
    })
  }
  for (const [day, group] of passes) {
    const newest = group.reduce((a, b) => (Date.parse(b.startedAt) > Date.parse(a.startedAt) ? b : a))
    const took = elapsed(newest.startedAt, newest.finishedAt)
    out.push({
      key: `hook:${hook}:${day}`, at: Date.parse(newest.startedAt), tone: 'done', session: '', steps: [],
      text: group.length > 1 ? `${label} · ${group.length} runs passed${day === today ? ' today' : ''}` : `${label} · passed${took ? ` in ${took}` : ''}`,
    })
  }
  return byNewest(out)
}

// reviewEntries lists the room's verdicts; each opens the reviewer behind it.
export function reviewEntries(changes: ChangeRow[]): Entry[] {
  return byNewest(changes.flatMap((c, n) => {
    if (!c.reviewAt) return []
    const verdict = c.verdict ? c.verdict.replaceAll('-', ' ') : 'reviewed'
    const name = c.number ? `#${c.number}` : c.title || c.key || 'local change'
    return [{
      key: `verdict:${c.key ?? c.number ?? n}:${c.reviewAt}`, at: Date.parse(c.reviewAt),
      text: `${verdict} ${name}${c.number && c.title ? ` · ${c.title}` : ''}`,
      tone: c.verdict === 'changes' ? 'failed' : 'done', session: c.reviewerSession ?? '', steps: [],
    }]
  }))
}
