// The native harness of a provider, and the sign-in flows of one account. An
// account belongs to a provider, and until credentials are shared a provider
// below Pi is configured through the harness that serves it.
import { harnessMeta, harnessRowState, recommendedVersion } from '../harnesses/state.ts'
import type { HarnessSnapshot, PrivateLogin } from '../../api/harness.ts'
import type { Provider } from '../../api/account.ts'

// NATIVE_HARNESS maps a provider to the harness that signs it in itself. Pi
// serves every provider, so it is the fallback and never listed here.
const NATIVE_HARNESS: Partial<Record<Provider, string>> = {
  anthropic: 'claude-code',
  'opencode-go': 'opencode',
  'meta-muse': 'muse',
}

// nativeHarness names the harness that signs a provider in itself, or '' when
// only Pi does. Source: store.ProviderFor and the adapters in internal/harness.
export function nativeHarness(provider: Provider): string {
  return NATIVE_HARNESS[provider] ?? ''
}

export function harnessLabel(harness: string): string {
  return harnessMeta(harness).name
}

// OPENCODE_KEY maps a provider to the provider ID OpenCode keeps its API key
// for, or '' when OpenCode takes no key for it. Source: store.ProviderFor.
const OPENCODE_KEY: Partial<Record<Provider, string>> = {
  'opencode-go': 'opencode-go',
  anthropic: 'anthropic',
  chatgpt: 'openai',
  xai: 'xai',
  deepseek: 'deepseek',
  zai: 'zai',
}

// openCodeKey names the OpenCode integration of a provider, or '' when it has
// no API key there.
export function openCodeKey(provider: Provider): string {
  return OPENCODE_KEY[provider] ?? ''
}

// harnessPin is the exact version a sign-in installs or uses: Dyna's copy, or
// the version Dyna recommends. Empty means Dyna knows no version, so the owner
// must install the harness first.
export function harnessPin(snapshot: HarnessSnapshot | null | undefined, harness: string): string {
  const row = harnessRowState(harness, snapshot)
  return row.version || recommendedVersion(row)
}

// sharedLogin reports whether signing in also changes the owner's own CLI
// login, which needs a confirmation first.
export function sharedLogin(snapshot: HarnessSnapshot | null | undefined, harness: string): boolean {
  const capability = (snapshot?.capabilities ?? []).find((c) => c.harness === harness)
  return !!(capability?.sharedPersonalLogin ?? harness === 'muse')
}

// runningFlow finds the sign-in that belongs to this dialog, so closing and
// reopening it keeps the saved sign-in reachable. A sign-in again names its
// account: Pi's provider card names it as a target, a native card as its
// account. A new account has neither, and a new Pi card runs without one.
export function runningFlow(flows: PrivateLogin[] | null | undefined, account: { id: string } | null | undefined): PrivateLogin | null {
  const all = flows ?? []
  if (account?.id) return all.find((flow) => flow.accountId === account.id || flow.target === account.id) ?? null
  return all.find((flow) => flow.harness === 'pi' && !flow.target) ?? all.find((flow) => !flow.target) ?? null
}
