// The private harness runtimes. Source: internal/harness/runtime
// (harness_runtime.go, accounts.go, bundle.go, recommended.go, runtime.go).

// Bundle is runtime.Bundle, one installed copy of a harness.
export interface Bundle {
  harness: string
  version: string
  executable: string
  directory: string
}

// HarnessAccount is runtime.NativeAccount, a sign-in a harness keeps. repos
// lists the repositories it is limited to; empty means any.
export interface HarnessAccount {
  id: string
  harness: string
  label: string
  home: string
  identity: string
  status: string
  active: boolean
  expiresAt?: string
  repos: string[] | null
}

// Doctor is runtime.Doctor, the status check of one profile.
export interface Doctor {
  profile: string
  harness: string
  binary: string
  version: string
  home: string
  accountId: string
  status: string
  reason: string
  switchPending: boolean
  runningTurns: number
  warnings?: string[]
}

// Capability is runtime.Capability, what Dyna supports for a harness.
export interface Capability {
  harness: string
  status: string
  reason: string
  installSupported: boolean
  accountBlocked: string
  mcp: boolean
  skills: boolean
  skillsPinned: boolean
  concurrent: boolean
  sharedPersonalLogin: boolean
}

// RecommendedVersion is runtime.RecommendedVersion. status is pinned,
// resolved, unavailable or unsupported.
export interface RecommendedVersion {
  harness: string
  version?: string
  source: string
  status: string
  reason?: string
}

// HarnessSnapshot is runtime.PrivateRuntimeSnapshot. installing lists the
// harnesses whose CLI is being installed on first use.
export interface HarnessSnapshot {
  bundles: Bundle[] | null
  accounts: HarnessAccount[] | null
  profiles: Doctor[] | null
  capabilities: Capability[] | null
  selectedVersions: Record<string, string> | null
  recommendedVersions: RecommendedVersion[] | null
  installing: string[] | null
}

// SignInState is runtime.SignInState: the step of an in-app sign-in, as the
// shared card shows it. It never holds credentials, only the link and the code.
export interface SignInState {
  phase: string
  url?: string
  code?: string
  message?: string
  acceptsPaste?: boolean
  secretInput?: boolean
  choices?: { id: string; label: string }[]
}

// PrivateLogin is runtime.PrivateLogin: the flow a harness account waits on.
export interface PrivateLogin {
  accountId: string
  warning?: string
  signIn?: SignInState
}

// PersonalLogin is runtime.PersonalLogin: the owner's own native login. saved
// means a Dyna account already holds the same identity.
export interface PersonalLogin {
  harness: string
  label: string
  saved: boolean
}

// PreferenceKey and PreferencePreview are runtime.PreferenceKey and
// runtime.PreferencePreview: the safe settings Dyna can import for an account.
export interface PreferenceKey {
  key: string
  status: string
}
export interface PreferencePreview {
  accountId: string
  harness: string
  hash: string
  keys: PreferenceKey[]
}

// ProviderKey and ProviderKeysResult are native.ProviderKey and
// runtime.ProviderKeysResult. They carry names only, never a key.
export interface ProviderKey {
  id: string
  name: string
}
export interface ProviderKeysResult {
  providers: ProviderKey[]
  choices?: ProviderKey[]
}

// ManagedMCP and ManagedSkill are config.ManagedMCP and config.ManagedSkill,
// the tools Dyna adds to harnesses; HarnessConfig is config.HarnessConfig.
export interface ManagedMCP {
  name: string
  command?: string
  args?: string[]
  envKeys?: string[]
  url?: string
  enabledHarnesses: string[]
}
export interface ManagedSkill {
  name: string
  source: string
  enabledHarnesses: string[]
}
export interface HarnessConfig {
  mcp: ManagedMCP[]
  skills: ManagedSkill[]
}

// PrivateRuntimeSwitchControl is runtime.PrivateRuntimeSwitchControl: an
// account switch waiting on running turns.
export interface PrivateRuntimeSwitchControl {
  harness: string
  requestId: string
  targetAccountId: string
  phase: string
  pending: boolean
  runningTurns: number
  canCancel: boolean
  canStop: boolean
}
