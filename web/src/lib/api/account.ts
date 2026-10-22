// Account usage and the model catalog. Sources: internal/account/service/types.go,
// internal/account/store/store.go and internal/profile/compact.go.

// Plan is store.Plan.
export interface Plan {
  id: string
  label: string
}

// UsageWindow is store.Window, one quota window of an account.
export interface UsageWindow {
  unavailableReason?: string
  period?: string
  periodStart?: string
  id: string
  label: string
  usedPercent?: number
  used?: number
  limit?: number
  unit?: string
  resetsAt?: string
  // Added by the client from the window's own fields.
  amountLabel?: string
  remainingPercent?: number
}

// UsageAccount is service.Account, an account as Usage shows it. Times are ISO strings.
export interface UsageAccount {
  usageError?: string
  expiresAt?: string
  refreshMessage?: string
  quotaBlocked: boolean
  quotaBlockedUntil?: string
  blockingQuotaWindows?: string[]
  id: string
  harness: string
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

// UsageSnapshot is service.Snapshot.
export interface UsageSnapshot {
  accounts: UsageAccount[] | null
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
