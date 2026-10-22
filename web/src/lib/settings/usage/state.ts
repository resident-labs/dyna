// Presentation only. Account identity and allowance arithmetic belong to the host.
import type { UsageAccount, UsageWindow } from '../../api/account.ts'
import { quotaBlock } from './reset.ts'

// The account fields these helpers read. The engine sends UsageAccount; a
// test or a stand-in row names only the fields it has.
export type AccountLike = Partial<Omit<UsageAccount, 'plan'>> & { plan?: UsageAccount['plan'] | string | null }

const ICON_IDS: Record<string, string> = { 'claude-code': 'claude' }
export function harnessIcon(harness: string): string {
  return ICON_IDS[harness] ?? harness
}
const HARNESS_NAMES: Record<string, string> = { 'claude-code': 'Claude Code', codex: 'Codex', grok: 'Grok', opencode: 'OpenCode', antigravity: 'Antigravity', muse: 'Muse', custom: 'Custom harness', mock: 'Mock' }
export function harnessName(harness: string | undefined): string {
  return HARNESS_NAMES[harness ?? ''] ?? harness ?? ''
}
const OPENCODE_INTEGRATIONS: Record<string, string> = { 'opencode-go': 'OpenCode Go', opencode: 'OpenCode Zen' }
export function accountLabel(account?: AccountLike | null): string {
  const label = account?.label || ''
  if (account?.harness === 'opencode' && (!label.trim() || label.trim() === 'OpenCode account')) {
    const integration = account.integration || ''
    return OPENCODE_INTEGRATIONS[integration] || (integration ? `OpenCode · ${integration}` : 'OpenCode account')
  }
  return label || harnessName(account?.harness)
}
const finite = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value)
export function remainingPercent(window?: Partial<UsageWindow> | null): number | null {
  const remaining = window?.remainingPercent, used = window?.usedPercent
  if (finite(remaining) && remaining >= 0 && remaining <= 100) return remaining
  if (finite(used) && used >= 0) return Math.max(0, 100 - used)
  return null
}
export function percent(value?: number | null): string {
  return finite(value) ? `${new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 }).format(value)}%` : 'Not reported'
}
export function dateTime(value?: string | null): string {
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '' : date.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })
}
export function observation(account?: AccountLike | null): string {
  const at = account?.observedAt?.startsWith('0001-') ? '' : dateTime(account?.observedAt)
  return [account?.source, at ? `Checked ${at}` : '', account?.stale ? 'Stale report' : '', account?.usageError ? (account?.rateLimitedUntil ? account.usageError : `Usage unavailable: ${account.usageError}`) : '', account?.refreshMessage || '', account?.rateLimitedUntil && new Date(account.rateLimitedUntil) > new Date() ? `Retry after ${dateTime(account.rateLimitedUntil)}` : ''].filter(Boolean).join(' · ')
}
export function needsSignIn(account?: AccountLike | null): boolean {
  return !!(account?.needsLogin || account?.status === 'not signed in' || account?.status === 'needs_auth' || account?.status === 'needs-sign-in')
}
export function accountStatus(account?: AccountLike | null, plan = '', now = Date.now()): string {
  if (needsSignIn(account)) return 'Needs sign-in'
  const block = blockedNow(account, now)
  if (block) {
    const label = block.ids.includes('seven_day') ? 'Weekly limit reached' : 'Session limit reached'
    const reset = dateTime(block.reset)
    return reset ? `${label} · Resets ${reset}` : label
  }
  const noPlan = account?.harness === 'opencode' ? '' : 'Plan not reported'
  // TODO: legacy/shim remove later — a plan sent as a bare string by older hosts; remove when no host sends one.
  if (account?.plan) return typeof account.plan === 'string' ? account.plan : account.plan.label || plan || noPlan
  if (plan && [undefined, 'ready', 'authenticated', 'configured'].includes(account?.status)) return plan
  const byStatus: Record<string, string> = { ready: noPlan, 'not-installed': 'Not installed', configured: noPlan ? `Configured · ${noPlan}` : 'Configured', authenticated: noPlan, unavailable: 'Temporarily unavailable', unknown: 'Account not reported' }
  return byStatus[account?.status ?? ''] ?? noPlan
}
// blockedNow reads the windows against the clock, so a page that stays open stops
// showing a limit whose every blocking window has reset. A reading without
// windows falls back to the host's own decision, which expires the same way.
function blockedNow(account: AccountLike | null | undefined, now: number): { ids: string[]; reset: string } | null {
  const block = quotaBlock(account?.windows ?? undefined, account?.harness, now)
  if (block) return { ids: block.ids, reset: new Date(block.until).toISOString() }
  if (account?.windows?.length) return null
  const until = Date.parse(account?.quotaBlockedUntil ?? '')
  if (!account?.quotaBlocked || (Number.isFinite(until) && until <= now)) return null
  return { ids: account?.blockingQuotaWindows ?? [], reset: account?.quotaBlockedUntil ?? '' }
}
// Every async read uses a ticket. Invalidating on close/context change also rejects
// a delayed result for a second request for the same account.
export function requestGate() {
  let version = 0
  return { next: () => ++version, current: (ticket: number) => ticket === version, invalidate: () => ++version }
}
export function allowanceLabel(window?: Partial<UsageWindow> | null): string {
  if (window?.amountLabel) return window.amountLabel
  if (!finite(window?.used) || !finite(window?.limit)) return window?.unavailableReason || ''
  const currency = ['USD', 'usd'].includes(window.unit ?? '')
  const format = new Intl.NumberFormat(undefined, currency ? { style: 'currency', currency: 'USD', maximumFractionDigits: 2 } : { maximumFractionDigits: 3 })
  return `${format.format(window.used)} of ${format.format(window.limit)}${currency || !window.unit ? '' : ' ' + window.unit} used`
}

// shortLabel turns a provider's window label into the model group and the
// period, such as "Gemini Models: Weekly Limit Remaining (weekly)" into
// "Gemini weekly". A label with no known period stays as it is.
const PERIODS: [RegExp, string][] = [[/\b(5h|5-hour|five hour)\b/i, '5-hour'], [/\b(weekly|7-day|week)\b/i, 'weekly']]
export function shortLabel(label: string): string {
  const [group = '', rest = ''] = label.includes(':') ? label.split(/:\s*/, 2) : ['', label]
  const period = PERIODS.find(([re]) => re.test(rest))
  if (!period) return label
  const name = (group || rest.replace(/\(.*?\)/g, '').replace(new RegExp(period[0].source, 'gi'), '').replace(/\b(session|limit|remaining|usage)\b/gi, ''))
    .replace(/\s+models?\s*$/i, '').replace(/\s+and\s+/i, '/').trim()
  return name ? `${name} ${period[1]}` : period[1].charAt(0).toUpperCase() + period[1].slice(1)
}

// Only a host-reported plan joins a subscription. Account identities and
// windows stay separate; a common plan does not establish a common balance.
export const SUBSCRIPTIONS: { id: string; name: string; icon: string }[] = [
  { id: 'x-subscription', name: 'X Subscription (weekly)', icon: 'grok' },
  { id: 'grok-legacy', name: 'Legacy (monthly)', icon: 'grok' },
]
// A row of the Usage list: one account, or the accounts of one shared subscription.
export interface UsageGroup<A extends AccountLike = UsageAccount> {
  id: string
  name: string
  icon: string
  plan?: string
  accounts: A[]
}
export function usageGroups<A extends AccountLike>(accounts: A[] | null | undefined): UsageGroup<A>[] {
  const list = accounts ?? []
  const subscription = (account: A) => SUBSCRIPTIONS.find(sub => (typeof account.plan === 'object' ? account.plan?.id : undefined) === sub.id)
  return [
    ...SUBSCRIPTIONS.flatMap(sub => {
      const members = list.filter(account => subscription(account) === sub)
      return members.length ? [{ ...sub, plan: sub.name, accounts: members }] : []
    }),
    ...list.filter(account => !subscription(account)).map(account => ({
      id: account.id ?? '', name: accountLabel(account), icon: harnessIcon(account.harness ?? ''), accounts: [account],
    })),
  ]
}
export function unreadText(account?: AccountLike | null): string {
  if (needsSignIn(account)) return 'Needs sign-in'
  if (account?.status === 'not-installed') return 'Not installed'
  if (typeof account?.plan === 'object' && account.plan?.id === 'grok-legacy') return 'Legacy plan, usage not read'
  return 'Live remaining not reported'
}

export function windowReset(window?: Partial<UsageWindow> | null): string {
  const reset = dateTime(window?.resetsAt)
  if (!reset) return ''
  const passed = new Date(window?.resetsAt ?? '').getTime() <= Date.now()
  return `${passed && window?.label?.endsWith(' (idle)') ? 'Idle since' : passed ? 'Reset' : 'Resets'} ${reset}`
}
