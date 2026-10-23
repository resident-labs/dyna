// What the Dyna home screen shows. The functions are pure; Home.svelte loads
// the data and draws the result.
import { accountLabel, accountStatus, needsSignIn, remainingPercent, SUBSCRIPTIONS, usageGroups } from '../settings/usage/state.ts'
import type { AccountLike, UsageGroup } from '../settings/usage/state.ts'
import { HARNESS_ORDER, harnessRowState } from '../settings/harnesses/state.ts'
import type { HarnessSnapshot } from '../api/harness.ts'

// One account that needs the owner: a login or a quota block.
export interface AccountAlert {
  id: string
  text: string
  action: string
}

export interface HomeAccounts<A extends AccountLike = AccountLike> {
  cards: UsageGroup<A>[]
  alerts: AccountAlert[]
}

// The plan a subscription joins. A host may still send a plan as a bare string.
const planId = (account: AccountLike) => (typeof account.plan === 'object' ? account.plan?.id : undefined)

// accountRows splits the active accounts: one that works and reports usage
// is a usage card, one that needs a login or is quota-blocked is an alert row.
// Known subscriptions also show their period when no percentage is reported.
export function accountRows<A extends AccountLike>(snapshot?: { accounts?: A[] | null } | null): HomeAccounts<A> {
  const cards: A[] = []
  const alerts: AccountAlert[] = []
  for (const a of snapshot?.accounts ?? []) {
    if (!a.active) continue
    const login = needsSignIn(a)
    if (login || a.quotaBlocked) alerts.push({ id: a.id ?? '', text: `${accountLabel(a)}: ${accountStatus(a)}`, action: login ? 'Log in' : 'Details' })
    else if (SUBSCRIPTIONS.some((sub) => sub.id === planId(a)) || a.windows?.some((w) => remainingPercent(w) !== null)) cards.push(a)
  }
  return { cards: usageGroups(cards), alerts }
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
}

export interface RootQuestionRow {
  key: string
  room: HomeRoom
  text?: string
}

// rootQuestions lists the open questions root asked the human. Agents ask
// root, not the human, so the home screen shows nothing else that waits.
export function rootQuestions(rooms: HomeRoom[], questionsByRoom: Record<string, HomeQuestion[] | undefined>): RootQuestionRow[] {
  const rows: RootQuestionRow[] = []
  for (const room of rooms) {
    if (room.archived) continue
    for (const q of questionsByRoom[room.id] ?? []) {
      if (q.state === 'open' && q.to === 'owner' && q.asker === 'owner' && !q.human) rows.push({ key: `${room.id}:${q.id}`, room, text: q.text })
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
