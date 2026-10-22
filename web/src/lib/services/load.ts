// Loads what the service cards need, each call on its own: one that fails
// (a caller without admin scope cannot read settings) leaves its part null
// and the others still show.
import type { HistoryRow, QueueRow, ReviewerInfo, ServiceRow, UsageRow } from './cards.ts'
import { readHistory, readQueue, readReviewer, readServices, readSettings, readUsage, readVoice } from './read.ts'

export interface ServiceApi {
  listServices(room: string): Promise<unknown>
  reviewerStatus(room: string): Promise<unknown>
  roomMergeStatus(room: string): Promise<unknown>
  roomMergeHistory(room: string): Promise<unknown>
  settings(): Promise<unknown>
  usageRollup(room: string, from: string, to: string): Promise<unknown>
  voiceStatus(): Promise<unknown>
}

export type Part = 'services' | 'reviewer' | 'queue' | 'history' | 'settings' | 'usage' | 'voice'
const allParts: Part[] = ['services', 'reviewer', 'queue', 'history', 'settings', 'usage', 'voice']

export interface ServiceData {
  pending: Part[] // parts whose call has not answered yet; each card shows loading until its part arrives
  services: ServiceRow[]
  reviewer: ReviewerInfo | null
  queue: QueueRow[] | null
  history: HistoryRow[]
  settings: Record<string, string> | null
  usage: UsageRow[] | null
  voice: string | null
}

export const emptyData: ServiceData = { pending: allParts, services: [], reviewer: null, queue: null, history: [], settings: null, usage: null, voice: null }

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
  await Promise.all([
    load('services', () => api.listServices(room), readServices, (d, v) => ({ ...d, services: v })),
    load('reviewer', () => api.reviewerStatus(room), readReviewer, (d, v) => ({ ...d, reviewer: v })),
    load('queue', () => api.roomMergeStatus(room), readQueue, (d, v) => ({ ...d, queue: v })),
    load('history', () => api.roomMergeHistory(room), readHistory, (d, v) => ({ ...d, history: v })),
    load('settings', () => api.settings(), readSettings, (d, v) => ({ ...d, settings: v })),
    load('usage', () => api.usageRollup(room, dayStart.toISOString(), now.toISOString()), readUsage, (d, v) => ({ ...d, usage: v })),
    load('voice', () => api.voiceStatus(), readVoice, (d, v) => ({ ...d, voice: v })),
  ])
  return data
}
