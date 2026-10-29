// Account usage and the model catalog. Sources: internal/account/service/types.go,
// internal/account/store/store.go and internal/profile/compact.go.

// Provider is store.Provider, the sign-in service an account belongs to. A
// harness consumes an account; it does not own it. Empty means the saved row
// names no provider this release knows.
export type Provider = 'anthropic' | 'chatgpt' | 'kimi-code' | 'xai' | 'meta-muse' | 'opencode-go' | 'deepseek' | 'zai' | ''

// AccountKind is store.Kind, how an account signed in.
export type AccountKind = 'oauth' | 'api_key' | ''

// Plan is store.Plan.
export interface Plan {
  id: string
  label: string
}

// UsageReason is store.UsageReason, why an account has no current reading.
// The app chooses a designed message for each reason.
export type UsageReason = 'not-readable' | 'expired' | 'secret' | 'provider-error' | 'rate-limited' | 'no-windows'

// RenewalResult is store.RenewalResult, the outcome of an engine renewal.
export type RenewalResult = 'renewed' | 'no_credential' | 'invalid_grant' | 'transient'

// AccountRenewal is store.Renewal. The web app words the typed result itself.
export interface AccountRenewal {
  accountId: string
  harness: string
  provider?: Provider
  attemptedAt: string
  result: RenewalResult
  reason?: string
}

// WindowReason is store.WindowReason, why a window carries no reading. The app
// chooses a designed message for each reason, so reader prose never reaches it.
export type WindowReason = 'not-reported' | 'probable-reset'

// UsageWindow is store.Window, one quota window of an account.
export interface UsageWindow {
  reason?: WindowReason
  period?: string
  periodStart?: string
  id: string
  label: string
  usedPercent?: number
  used?: number
  limit?: number
  // balance is a remaining money amount when the provider reports a balance
  // instead of a quota window. unit names its currency.
  balance?: number
  unit?: string
  resetsAt?: string
  // Added by the client from the window's own fields.
  amountLabel?: string
  remainingPercent?: number
}

// Account is service.Account, one saved provider sign-in. Times are ISO strings.
export interface Account {
  usageError?: string
  usageReason?: UsageReason
  expiresAt?: string
  refreshMessage?: string
  lastRenewal?: AccountRenewal
  quotaBlocked: boolean
  quotaBlockedUntil?: string
  blockingQuotaWindows?: string[]
  id: string
  harness: string
  provider?: Provider
  kind?: AccountKind
  repos: string[] | null
  label: string
  providerId: string
  routeId?: string
  active: boolean
  status: string
  plan?: Plan
  source: string
  observedAt: string
  rateLimitedUntil?: string
  stale: boolean
  profiles: string[] | null
  windows: UsageWindow[] | null
  integration?: string
  needsLogin: boolean
}

// ScopeAccount is service.ScopeAccount: an account with its place in the scope
// the page asked about. activeHere is the account its provider uses in that
// scope; active is the global one.
export interface ScopeAccount extends Account {
  activeHere: boolean
}

// ProviderAccounts is service.ProviderAccounts: every listed account of one
// provider.
export interface ProviderAccounts {
  provider: Provider
  accounts: ScopeAccount[]
}

// AccountProblemReason is runtime.AccountProblemReason, why the list skipped a
// saved row.
export type AccountProblemReason = 'unknown-harness' | 'unsafe-home' | 'missing-home' | 'unreadable'

// AccountProblem is runtime.AccountProblem, one saved row the list skipped. It
// is a result for that row alone, never a reason to fail the whole list.
export interface AccountProblem {
  id: string
  harness?: string
  label?: string
  reason: AccountProblemReason
}

// AccountsSnapshot is service.AccountsSnapshot, the account view grouped by
// provider. scope is the room it was taken for, or empty for the global one.
export interface AccountsSnapshot {
  scope?: string
  providers: ProviderAccounts[] | null
  problems: AccountProblem[] | null
  checkedAt: string
  message?: string
}

// Effort is adapters.Effort, one reasoning level a model accepts. Its args go
// in a profile's extraArgs; a harness that encodes the level in the model ID
// sets model instead.
export interface Effort {
  id: string
  label?: string
  default?: boolean
  args: string[] | null
  model?: string
}

// ModelLimit is service.Limit.
export interface ModelLimit {
  label: string
  value: string
  source: string
}

// CatalogModel is service.Model.
export interface CatalogModel {
  id: string
  label: string
  providerId?: string
  integrationId?: string
  publisher?: string
  context?: number
  input?: number
  output?: number
  category: string
  source: string
  description?: string
  limits: ModelLimit[] | null
  efforts?: Effort[]
}

// CompactRule is profile.CompactRule: what a harness accepts for the compact field.
export interface CompactRule {
  window?: boolean
  min?: number
  max?: number
  trigger?: boolean
  reason?: string
}

// Compact is profile.Compact: the auto-compact threshold of a profile.
export interface Compact {
  tokens: number
  windows?: Record<string, number>
}

// Catalog is service.Catalog, a harness's models.
export interface Catalog {
  models: CatalogModel[] | null
  compact: CompactRule
  source: string
  observedAt: string
  message?: string
}
