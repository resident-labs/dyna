// What the Dyna home screen shows. The functions are pure; Home.svelte loads
// the data and draws the result.
import { accountLabel, accountStatus, needsSignIn, remainingPercent } from '../settings/accounts/state.ts'
import { requestFilter } from '../requests/requests.ts'
import type { AccountLike } from '../settings/accounts/state.ts'
import type { Provider, ProviderAccounts, ScopeAccount } from '../api/account.ts'
import { HARNESS_ORDER, harnessRowState } from '../settings/harnesses/state.ts'
import type { HarnessSnapshot } from '../api/harness.ts'

// One account that needs the owner: a login or a quota block.
export interface AccountAlert {
  id: string
  text: string
  action: string
}

export interface HomeAccounts {
  cards: ProviderAccounts[]
  alerts: AccountAlert[]
}

// A plan that reports its period instead of a percentage. A host may still send
// a plan as a bare string.
const PERIODIC_PLANS = new Set(['x-subscription', 'grok-legacy'])
const planId = (account: AccountLike) => (typeof account.plan === 'object' ? account.plan?.id : undefined)

// accountRows splits the accounts the global scope uses: one that works and
// reports usage is a card of its provider, one that needs a login or is
// quota-blocked is an alert row. A plan that reports its period still gets a
// card, so a weekly quota without a percentage stays visible.
export function accountRows(snapshot?: { providers?: { provider?: Provider; accounts?: ScopeAccount[] }[] | null } | null): HomeAccounts {
  const cards: ProviderAccounts[] = []
  const alerts: AccountAlert[] = []
  for (const group of snapshot?.providers ?? []) {
    const shown: ScopeAccount[] = []
    for (const a of group.accounts ?? []) {
      if (!a.activeHere) continue
      const login = needsSignIn(a)
      if (login || a.quotaBlocked) alerts.push({ id: a.id, text: `${accountLabel(a)}: ${accountStatus(a)}`, action: login ? 'Log in' : 'Details' })
      else if (PERIODIC_PLANS.has(planId(a) ?? '') || a.windows?.some((w) => remainingPercent(w) !== null)) shown.push(a)
    }
    if (shown.length) cards.push({ provider: group.provider ?? '', accounts: shown })
  }
  return { cards, alerts }
}

// One row of what has a newer version.
export interface UpdateRow {
  id: string
  name: string
  current: string
  latest: string
}

// updateRows lists what has a newer version: Dyna itself (an update notice
// with a latest version) and each installed harness whose recommended
// version is newer than Dyna's copy.
export function updateRows(notice?: { current?: string; latest?: string } | null, runtimes?: Partial<HarnessSnapshot> | null): UpdateRow[] {
  const updates: UpdateRow[] = notice?.latest ? [{ id: 'dyna', name: 'dyna', current: notice.current ?? '', latest: notice.latest }] : []
  if (runtimes) {
    for (const h of HARNESS_ORDER) {
      const row = harnessRowState(h.id, runtimes)
      if (row.update) updates.push({ id: h.id, name: h.id, current: row.version, latest: row.update })
    }
  }
  return updates
}

// The fields the home screen reads from a room and its questions.
export interface HomeRoom {
  id: string
  name?: string
  archived?: boolean
}

export interface HomeQuestion {
  id: string
  state: string
  to: string
  asker: string
  text?: string
  human?: boolean
  askerName?: string
  kind?: 'question' | 'terminal'
  terminal?: { stage?: string }
}

export interface HomeRequestRow {
  key: string
  room: HomeRoom
  text?: string
  kind?: 'question' | 'terminal'
  askerName?: string
}

// homeRequests lists root's open questions and terminal requests waiting for
// the owner. Ordinary questions from other agents still go to root, not Home.
export function homeRequests(rooms: HomeRoom[], questionsByRoom: Record<string, HomeQuestion[] | undefined>): HomeRequestRow[] {
  const rows: HomeRequestRow[] = []
  for (const room of rooms) {
    if (room.archived) continue
    for (const q of questionsByRoom[room.id] ?? []) {
      const rootQuestion = q.state === 'open' && q.to === 'owner' && q.kind !== 'terminal' && q.asker === 'owner' && !q.human
      const terminalRequest = q.to === 'owner' && q.kind === 'terminal' && requestFilter(q) === 'open'
      if (rootQuestion || terminalRequest) rows.push({ key: `${room.id}:${q.id}`, room, text: q.text, kind: q.kind, askerName: q.askerName })
    }
  }
  return rows
}

// The sources Home counts before it may say nothing needs the owner.
export interface HomeClear {
  usage?: unknown
  runtimes?: unknown
  questionsLoaded?: boolean
  noticesLoaded?: boolean
  cards?: unknown
  asked?: unknown
  alerts?: unknown
  updates?: unknown
}

// allClear reports whether Home may say nothing needs the owner: every source
// has loaded successfully, and none has a card, question, alert or update.
// A source that is still loading, or failed, never counts as empty.
export function allClear({ usage, runtimes, questionsLoaded, noticesLoaded, cards, asked, alerts, updates }: HomeClear): boolean {
  return !!(usage && runtimes && questionsLoaded && noticesLoaded) && !cards && !asked && !alerts && !updates
}
