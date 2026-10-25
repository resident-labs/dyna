// Presentation only. Account identity and allowance arithmetic belong to the host.
import type { Account, AccountKind, AccountProblem, Provider, ScopeAccount, UsageWindow } from '../../api/account.ts'
import { quotaBlock } from './reset.ts'

// The account fields these helpers read. The engine sends Account; a test or a
// stand-in row names only the fields it has.
export type AccountLike = Partial<Omit<Account, 'plan'>> & { plan?: Account['plan'] | string | null }

// PROVIDERS is the provider list of the add form, in the order the design
// ranks them. Source: the Provider constants in internal/account/store/store.go.
export const PROVIDERS: Provider[] = ['anthropic', 'chatgpt', 'kimi-code', 'xai', 'meta-muse', 'opencode-go', 'deepseek', 'zai']

const PROVIDER_NAMES: Record<string, string> = {
  anthropic: 'Anthropic',
  chatgpt: 'ChatGPT',
  'kimi-code': 'Kimi Code',
  xai: 'xAI',
  'meta-muse': 'Meta Muse',
  'opencode-go': 'OpenCode Go',
  deepseek: 'DeepSeek',
  zai: 'Z.ai',
}

// providerName words a provider for a heading. A row that names no provider
// this release knows gets its own heading rather than a blank one.
export function providerName(provider?: string | null): string {
  if (!provider) return 'No provider'
  return PROVIDER_NAMES[provider] ?? provider
}

const OPENCODE_INTEGRATIONS: Record<string, string> = { 'opencode-go': 'OpenCode Go', opencode: 'OpenCode Zen' }

// accountLabel names an account. A saved row without a label falls back to its
// provider, which is what the account signs in to.
export function accountLabel(account?: AccountLike | null): string {
  const label = account?.label || ''
  if (account?.harness === 'opencode' && (!label.trim() || label.trim() === 'OpenCode account')) {
    const integration = account.integration || ''
    return OPENCODE_INTEGRATIONS[integration] || (integration ? `OpenCode · ${integration}` : 'OpenCode account')
  }
  return label || providerName(account?.provider) || account?.harness || ''
}

// accountKindLabel says how an account signed in.
export function accountKindLabel(kind?: AccountKind | null): string {
  if (kind === 'api_key') return 'API key'
  if (kind === 'oauth') return 'Sign-in'
  return ''
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

// money formats a balance in its currency. A balance is money, not a percent.
export function money(value?: number | null, unit?: string): string {
  if (!finite(value)) return 'Not reported'
  if (['USD', 'usd'].includes(unit ?? '')) {
    return new Intl.NumberFormat(undefined, { style: 'currency', currency: 'USD', maximumFractionDigits: 2 }).format(value)
  }
  const formatted = new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 }).format(value)
  return unit ? `${formatted} ${unit.toUpperCase()}` : formatted
}

export function dateTime(value?: string | null): string {
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '' : date.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })
}

// observation says where a reading came from and how old it is.
export function observation(account?: AccountLike | null): string {
  const at = account?.observedAt?.startsWith('0001-') ? '' : dateTime(account?.observedAt)
  const retry = account?.rateLimitedUntil && new Date(account.rateLimitedUntil) > new Date() ? `Retry after ${dateTime(account.rateLimitedUntil)}` : ''
  return [account?.source, at ? `Checked ${at}` : '', account?.stale ? 'Stale report' : '', retry].filter(Boolean).join(' · ')
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

// usageUnavailableText says why an account shows no meters. It uses the typed
// reason, never raw reader text. A key account gets its own words: the
// provider publishes no quota for a key, so an empty meter there is expected.
export function usageUnavailableText(account?: AccountLike | null): string {
  if (needsSignIn(account)) return 'Sign in to read usage'
  switch (account?.usageReason) {
    case 'expired': return 'Sign in again to read usage'
    case 'secret': return 'Reader output rejected: contained a secret'
    case 'not-readable': return notReadableUsage(account?.provider)
    case 'rate-limited': return 'Usage is rate limited. Try again later'
    case 'provider-error': return 'Usage is temporarily unavailable'
    case 'no-windows': return 'The provider reported no usage windows'
  }
  if (account?.kind === 'api_key') return 'Usage unavailable for an API key'
  if (account?.status === 'not-installed') return 'Not installed'
  return 'Usage unavailable'
}

// notReadableUsage names the provider when Dyna cannot read its plan usage.
function notReadableUsage(provider?: string | null): string {
  if (provider === 'meta-muse') return 'Dyna cannot read Muse plan usage from this API key. Sign in to Muse through Pi to read it.'
  return 'This provider does not publish usage that Dyna can read.'
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

export function windowReset(window?: Partial<UsageWindow> | null): string {
  const reset = dateTime(window?.resetsAt)
  if (!reset) return ''
  const passed = new Date(window?.resetsAt ?? '').getTime() <= Date.now()
  return `${passed && window?.label?.endsWith(' (idle)') ? 'Idle since' : passed ? 'Reset' : 'Resets'} ${reset}`
}

// accountsOf flattens every provider group into one list.
export function accountsOf(snapshot?: { providers?: { provider?: Provider; accounts?: ScopeAccount[] }[] | null } | null): ScopeAccount[] {
  return (snapshot?.providers ?? []).flatMap((group) => group.accounts ?? [])
}

// reposLabel says how far an account's repo limit reaches.
export function reposLabel(account?: AccountLike | null): string {
  const n = account?.repos?.length ?? 0
  return n ? `${n} ${n === 1 ? 'repo' : 'repos'} only` : 'All repos'
}

// repoChips are the repos an account is limited to, for the card's chips.
export function repoChips(account?: AccountLike | null): string[] {
  return account?.repos ?? []
}

// parseRepoList turns the one-repo-per-line field into the list the engine takes.
export function parseRepoList(text: string): string[] {
  return text.split('\n').map((line) => line.trim()).filter(Boolean)
}

// problemText words a saved row the list skipped. The reason is a fixed engine
// value, so the page can name the fault without echoing a raw message.
export function problemText(problem: AccountProblem): string {
  const name = problem.label || 'A saved account'
  switch (problem.reason) {
    case 'unknown-harness':
      return `${name}: Dyna does not run this kind of sign-in in this release.`
    case 'unsafe-home':
      return `${name}: its saved home directory is not safe to open.`
    case 'missing-home':
      return `${name}: its saved home directory is gone.`
    default:
      return `${name}: Dyna could not read this saved account.`
  }
}

// selectionText words the place of an account in the scope the page shows.
export function selectionText(account: ScopeAccount, roomScope: boolean): string {
  if (account.activeHere) return 'In use'
  return roomScope && account.active ? 'Global choice' : ''
}
