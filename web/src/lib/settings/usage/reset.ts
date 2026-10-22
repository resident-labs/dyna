export interface WindowTiming {
  resetsAt?: string
  unavailableReason?: string
}

// A quota window as the account reading carries it.
export interface QuotaWindow extends WindowTiming {
  id?: string
  usedPercent?: number
  used?: number
  limit?: number
}

// The account fields that decide a block. The host serves the same decision as
// quotaBlocked, quotaBlockedUntil and blockingQuotaWindows.
export interface BlockedAccount {
  harness?: string
  windows?: readonly QuotaWindow[] | null
  quotaBlocked?: boolean
  quotaBlockedUntil?: string
}

// A block while any window that stops an account is exhausted with its reset
// still ahead.
export interface Block {
  ids: string[]
  until: number
}

// Mirrors service.ApplyBlockingQuota: only these windows stop an account, and
// only while the reading is at its limit and the reset is still ahead.
function blockingIDs(harness?: string): readonly string[] {
  return harness === 'muse' ? ['five_hour', 'seven_day', 'subscription'] : ['five_hour', 'seven_day']
}

function exhausted(window: QuotaWindow): boolean {
  if (typeof window.usedPercent === 'number' && window.usedPercent >= 100) return true
  return typeof window.used === 'number' && typeof window.limit === 'number' && window.limit > 0 && window.used >= window.limit
}

// quotaBlock is the host's blocking decision computed against the clock, so a
// page that stays open stops showing a limit that has already reset.
export function quotaBlock(windows: readonly QuotaWindow[] | null | undefined, harness: string | undefined, now = Date.now()): Block | null {
  const ids: string[] = []
  let until = 0
  for (const window of windows ?? []) {
    if (!blockingIDs(harness).includes(window.id ?? '')) continue
    const reset = Date.parse(window.resetsAt ?? '')
    if (!Number.isFinite(reset) || reset <= now || !exhausted(window)) continue
    ids.push(window.id ?? '')
    until = Math.max(until, reset)
  }
  return ids.length ? { ids, until } : null
}

// blockUntil is when the last blocking window resets, or NaN when nothing in the
// reading blocks the account or the reset is already behind us. A header that
// stays open wakes at this deadline, so an expired one must ask for no wake and
// let a later account deadline win.
export function blockUntil(account: BlockedAccount | null | undefined, now = Date.now()): number {
  const block = quotaBlock(account?.windows, account?.harness, now)
  if (block) return block.until
  // A reading with windows is decided by the windows above, as in the status line.
  if (account?.windows?.length) return Number.NaN
  const until = Date.parse(account?.quotaBlockedUntil ?? '')
  return account?.quotaBlocked && Number.isFinite(until) && until > now ? until : Number.NaN
}

// Mirrors service.resetWindows: a reading exactly at its reset is fresh.
// The host also marks projected windows whose numeric fields are absent.
const probablyResetReason = 'probably reset'

export function resetNotice(window: WindowTiming, observedAt: string, now = Date.now()): string {
  const reset = Date.parse(window.resetsAt ?? '')
  const observed = Date.parse(observedAt)
  if (window.unavailableReason !== probablyResetReason &&
      !(Number.isFinite(reset) && reset <= now && (!Number.isFinite(observed) || observed < reset))) return ''
  const checked = Number.isFinite(observed) && !observedAt.startsWith('0001-')
    ? new Date(observed).toLocaleString(undefined, { weekday: 'short', hour: 'numeric', minute: '2-digit' })
    : 'unknown'
  return `${probablyResetReason} · last checked ${checked}`
}
