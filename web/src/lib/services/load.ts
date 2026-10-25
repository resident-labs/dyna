// Loads what the service cards need, each call on its own: one that fails
// (a caller without admin scope cannot read settings) leaves its part null
// and the others still show.
import type { AccountsInfo, BudgetInfo, HookRow, HookRunRow, MergeHistoryRow as HistoryRow, NoticeRow, QueueRow, ReviewerInfo, RunReason, ServiceInfo as ServiceRow, SyncInfoByRepo, TestHealth, UsageRow } from './model.ts'
import { readAccounts, readBudget, readHookLog, readHookRuns, readHookSummary, readMergeState, readNotices, readSync, readTestHealth, readHistory, readReviewer, readServices, readSettings, readUsage, readVoice } from './read.ts'

export interface ServiceApi {
  listServices(room: string): Promise<unknown>
  reviewerStatus(room: string): Promise<unknown>
  mergeControlStatus(room: string): Promise<unknown>
  roomMergeHistory(room: string): Promise<unknown>
  settings(): Promise<unknown>
  usageRollup(room: string, from: string, to: string): Promise<unknown>
  voiceStatus(): Promise<unknown>
  notices(): Promise<unknown>
  roomCIBudget(room: string): Promise<unknown>
  accountsList(scope: string, refresh: boolean): Promise<unknown>
  ciHealth(room: string, runs: number): Promise<unknown>
  syncStatus(): Promise<unknown>
}

export type Part = 'services' | 'reviewer' | 'queue' | 'history' | 'settings' | 'usage' | 'voice' | 'hooks' | 'notices' | 'budget' | 'accounts' | 'tests' | 'sync'
export const allParts: Part[] = ['services', 'reviewer', 'queue', 'history', 'settings', 'usage', 'voice', 'hooks', 'notices', 'budget', 'accounts', 'tests', 'sync']

export interface ServiceData {
  pending: Part[] // parts whose call has not answered yet; each card shows loading until its part arrives
  services: ServiceRow[]
  reviewer: ReviewerInfo | null
  queue: QueueRow[] | null
  mergePaused: boolean | null
  history: HistoryRow[]
  settings: Record<string, string> | null
  usage: UsageRow[] | null
  voice: string | null
  hooks: HookRow[] | null
  runs: Record<string, HookRunRow[]>
  // reasons holds a failed hook run's log reason by run id.
  reasons: Record<string, RunReason>
  notices: NoticeRow[] | null
  budget: BudgetInfo | null
  accounts: AccountsInfo | null
  testHealth: TestHealth | null
  laneSync: SyncInfoByRepo | null
}

export const emptyData: ServiceData = { pending: allParts, services: [], reviewer: null, queue: null, mergePaused: null, history: [], settings: null, usage: null, voice: null, hooks: null, runs: {}, reasons: {}, notices: null, budget: null, accounts: null, testHealth: null, laneSync: null }

// A call that does not answer in this time counts as failed, so a card shows an error and never loads forever.
export const CALL_TIMEOUT_MS = 20000

const withTimeout = (f: () => Promise<unknown>, ms: number): Promise<unknown> =>
  new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('timed out')), ms)
    // A call that throws before it returns a promise counts as failed too.
    Promise.resolve().then(f).then(resolve, reject).finally(() => clearTimeout(timer))
  })

// loadServiceData runs the calls side by side. Each part is handed to onData
// as soon as its call answers, so a slow call (the merge queue can take
// seconds) does not hold back the other cards. A failed call keeps its
// previous part, so one bad answer does not blank a card that was readable.
// Only the first load leaves parts pending; a refresh never shows loading.
export async function loadServiceData(
  api: ServiceApi, room: string, now: Date, previous: ServiceData = emptyData,
  onData: (d: ServiceData) => void = () => {}, timeoutMs: number = CALL_TIMEOUT_MS,
  // The page supplies a repository-scoped hook call only when it has a repository.
  hookSummary: (() => Promise<unknown>) | null = null,
  // The run list of one hook; the page supplies it with the summary call.
  hookRuns: ((name: string) => Promise<unknown>) | null = null,
  // One run's log, which carries the reason a failed run gives.
  hookLog: ((name: string, id: string) => Promise<unknown>) | null = null,
): Promise<ServiceData> {
  const dayStart = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()))
  let data: ServiceData = { ...previous }
  // One synchronous step per answer, so two answers never overwrite each other.
  const settle = (name: Part, apply: (d: ServiceData) => ServiceData) => {
    const next = apply(data)
    data = { ...next, pending: next.pending.filter((p) => p !== name) }
    onData(data)
  }
  const load = <T>(name: Part, call: () => Promise<unknown>, read: (v: unknown) => T, set: (d: ServiceData, v: T) => ServiceData) =>
    withTimeout(call, timeoutMs).then(
      (raw) => settle(name, (d) => set(d, read(raw))),
      () => settle(name, (d) => d),
    )
  const hooks = hookSummary
    ? load('hooks', hookSummary, readHookSummary, (d, v) => ({ ...d, hooks: v }))
    : Promise.resolve(settle('hooks', (d) => ({ ...d, hooks: [] })))
  // Each hook's recent runs follow the summary, which names the hooks. A failed
  // list keeps that hook's previous runs.
  const runs = hooks.then(() => Promise.all((hookRuns ? data.hooks ?? [] : []).map((h) =>
    withTimeout(() => hookRuns?.(h.hook.name) ?? Promise.resolve(null), timeoutMs).then(
      (raw) => settle('hooks', (d) => ({ ...d, runs: { ...d.runs, [h.hook.name]: readHookRuns(raw) } })),
      () => {},
    ),
  )))
  // A failed run's reason lives in its log, so each failure the page has not
  // read yet gets one more call. The page keeps a reason it already has, so a
  // reload reads only new failures.
  const reasons = runs.then(() => Promise.all((hookLog ? unreadFailures(data) : []).map(({ hook, id }) =>
    withTimeout(() => hookLog?.(hook, id) ?? Promise.resolve(null), timeoutMs).then(
      (raw) => {
        const reason = readHookLog(raw)
        if (reason) settle('hooks', (d) => ({ ...d, reasons: { ...d.reasons, [id]: reason } }))
      },
      () => {},
    ),
  )))
  await Promise.all([
    load('services', () => api.listServices(room), readServices, (d, v) => ({ ...d, services: v })),
    load('reviewer', () => api.reviewerStatus(room), readReviewer, (d, v) => ({ ...d, reviewer: v })),
    load('queue', () => api.mergeControlStatus(room), readMergeState, (d, v) => v ? ({ ...d, ...v }) : d),
    load('history', () => api.roomMergeHistory(room), readHistory, (d, v) => ({ ...d, history: v })),
    load('settings', () => api.settings(), readSettings, (d, v) => ({ ...d, settings: v })),
    load('usage', () => api.usageRollup(room, dayStart.toISOString(), now.toISOString()), readUsage, (d, v) => ({ ...d, usage: v })),
    load('voice', () => api.voiceStatus(), readVoice, (d, v) => ({ ...d, voice: v })),
    load('notices', () => api.notices(), readNotices, (d, v) => ({ ...d, notices: v })),
    load('budget', () => api.roomCIBudget(room), readBudget, (d, v) => ({ ...d, budget: v })),
    load('accounts', () => api.accountsList('', false), readAccounts, (d, v) => ({ ...d, accounts: v })),
    load('tests', () => api.ciHealth(room, 30), readTestHealth, (d, v) => ({ ...d, testHealth: v })),
    load('sync', () => api.syncStatus(), readSync, (d, v) => ({ ...d, laneSync: v })),
    runs,
    reasons,
  ])
  return data
}

// unreadFailures names the failed runs whose log reason the page does not have.
function unreadFailures(d: ServiceData): { hook: string; id: string }[] {
  const out: { hook: string; id: string }[] = []
  for (const [hook, list] of Object.entries(d.runs)) {
    for (const run of list) {
      if ((run.result === 'error' || run.result === 'fail') && run.id && !d.reasons[run.id]) out.push({ hook, id: run.id })
    }
  }
  return out
}
