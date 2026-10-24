import type { Bundle, Capability, Doctor, HarnessAccount, HarnessSnapshot, PrivateLogin, RecommendedVersion } from '../../api/harness.ts'
import { errorText } from '../../errorText.ts'

// LoginFlow is a sign-in card that a harness account waits on. The pane keeps
// it while Settings closes or changes panes.
export type LoginFlow = PrivateLogin & { harness: string }

// Confirmation is a question the pane asks before a sign-in or switch goes ahead.
export interface Confirmation {
  kind: string
  title: string
  lines: string[]
  action: string
  danger: boolean
  alternative?: string
  run: () => void
  runAlternative?: () => void
}

// failText is the thrown value's message, or the fallback when it has none.
export const failText = (e: unknown, fallback: string): string => (e ? errorText(e) : fallback)

export interface HarnessMeta {
  id: string
  iconId: string
  name: string
  short: string
}
export const HARNESS_ORDER: HarnessMeta[] = [
  { id: 'claude-code', iconId: 'claude', name: 'Claude Code', short: 'Claude' },
  { id: 'codex', iconId: 'codex', name: 'Codex', short: 'Codex' },
  { id: 'opencode', iconId: 'opencode', name: 'OpenCode', short: 'OpenCode' },
  { id: 'antigravity', iconId: 'antigravity', name: 'Antigravity', short: 'Antigrav.' },
  { id: 'grok', iconId: 'grok', name: 'Grok', short: 'Grok' },
  { id: 'kimi-code', iconId: 'kimi-code', name: 'Kimi Code', short: 'Kimi' },
  { id: 'pi', iconId: 'pi', name: 'Pi', short: 'Pi' },
  { id: 'muse', iconId: 'muse', name: 'Muse', short: 'Muse' },
]

// Harnesses that take API keys for providers, kept in their private home.
export const takesProviderKeys = (id: string): boolean => ['pi', 'opencode'].includes(id)

// Pi offers its providers' OAuth logins alongside the API-key form.
export const hasSubscriptionLogin = (id: string): boolean => HARNESS_ORDER.some((harness) => harness.id === id)

// filterProviders narrows the picker to names or IDs that contain every typed word.
export function filterProviders<C extends { name: string; id: string }>(choices: C[] | null | undefined, query?: string | null): C[] {
  const words = String(query ?? '').toLowerCase().split(/\s+/).filter(Boolean)
  return (choices ?? []).filter((c) => words.every((w) => `${c.name} ${c.id}`.toLowerCase().includes(w)))
}

export function harnessMeta(id: string): HarnessMeta {
  return HARNESS_ORDER.find((h) => h.id === id) ?? { id, iconId: id, name: id, short: id }
}

export function exactVersion(value: unknown): value is string {
  return typeof value === 'string' && /^(?:(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?|[0-9]{4}\.[0-9]{2}\.[0-9]{2}-[A-Za-z0-9]+)$/.test(value.trim())
}

export function preferenceStatusLabel(st?: string): string {
  if (st === 'allowed') return 'will copy'
  if (st === 'unknown') return 'not imported (unknown)'
  return st || 'not imported'
}

// npm versions follow semantic precedence. Muse release and date identifiers
// use the vendor's numeric order. Build metadata never changes precedence.
export function newer(a: string, b: string): boolean {
  const left = a.split('+')[0] ?? ''
  const right = b.split('+')[0] ?? ''
  const vendor = /^(?:\d+\.\d+\.\d+-R\d+(?:\.\d+)?|\d{4}\.\d{2}\.\d{2}-[A-Za-z0-9]+)$/
  if (vendor.test(left) || vendor.test(right)) return left.localeCompare(right, undefined, { numeric: true }) > 0
  const parse = (value: string) => /^(\d+)\.(\d+)\.(\d+)(?:-(.+))?$/.exec(value)
  const x = parse(left)
  const y = parse(right)
  if (!x || !y) return false
  for (let i = 1; i <= 3; i++) {
    const delta = BigInt(x[i] ?? '0') - BigInt(y[i] ?? '0')
    if (delta) return delta > 0n
  }
  if (x[4] === y[4]) return false
  if (!x[4] || !y[4]) return !x[4]
  const xp = x[4].split('.')
  const yp = y[4].split('.')
  for (let i = 0; i < Math.max(xp.length, yp.length); i++) {
    const left = xp[i], right = yp[i]
    if (left === right) continue
    if (left === undefined || right === undefined) return right === undefined
    const xn = /^\d+$/.test(left)
    const yn = /^\d+$/.test(right)
    if (xn && yn) return BigInt(left) > BigInt(right)
    if (xn !== yn) return !xn
    return left > right
  }
  return false
}

export function recommendedVersion(row?: { recommendation?: RecommendedVersion | null } | null): string {
  const candidate = row?.recommendation
  return ['pinned', 'resolved'].includes(candidate?.status ?? '') && exactVersion(candidate?.version) ? candidate.version.trim() : ''
}

export function recoveredLogins<F extends { accountId: string; harness?: string }>(flows: F[] | null | undefined, accounts: HarnessAccount[] | null | undefined): (F & { harness: string })[] {
  return (flows ?? []).flatMap((flow) => {
    const harness = (accounts ?? []).find((account) => account.id === flow.accountId)?.harness ?? flow.harness
    return harness ? [{ ...flow, harness }] : []
  })
}

// signInStatus words the step of an in-app sign-in for the shared card. A
// failure repeats the engine's message, which names the missing step.
const SIGN_IN_STATUS: Record<string, string> = {
  waiting: 'Waiting for you to approve the sign-in.',
  verifying: 'Checking the sign-in…',
  succeeded: 'Signed in.',
  cancelled: 'Sign-in cancelled.',
}
export function signInStatus(state?: { phase?: string; message?: string } | null): string {
  if (state?.phase === 'waiting' && state.message) return state.message
  if (state?.phase === 'failed') return state.message || 'Sign-in did not finish. Try again.'
  return SIGN_IN_STATUS[state?.phase ?? ''] ?? 'Starting sign-in…'
}

export function accountNeedsSignIn(account?: { needsLogin?: boolean; status?: string } | null): boolean {
  return !!account && (!!account.needsLogin || ['not signed in', 'needs_auth', 'needs-sign-in'].includes(account.status ?? ''))
}

// accountScopeLabel says how far an account's repo limit reaches.
export function accountScopeLabel(account: HarnessAccount): string {
  const n = account.repos?.length ?? 0
  return n ? `${n} ${n === 1 ? 'repo' : 'repos'} only` : 'All repos'
}

// parseScopeList turns the one-repo-per-line field into the list the engine takes.
export function parseScopeList(text: string): string[] {
  return text.split('\n').map((line) => line.trim()).filter(Boolean)
}

// personalLoginChoice words the dialog that offers the user's own login. A login
// that Dyna already saved can only lead to another account.
export function personalLoginChoice(name: string, found: { saved?: boolean; label: string }): { title: string; lines: string[]; action: string; alternative: string } {
  if (found.saved) {
    return {
      title: `Sign in to ${name}`,
      lines: [`Your personal account ${found.label} is already saved in Dyna.`],
      action: 'Sign in with another account',
      alternative: '',
    }
  }
  return {
    title: `Sign in to ${name}`,
    lines: [`Dyna found your personal ${name} account ${found.label}.`, 'Use it, and Dyna copies the login once. Or sign in with another account.'],
    action: 'Use this account',
    alternative: 'Sign in instead',
  }
}

// What a harness row shows: its copy, sign-in, update and status.
export interface HarnessRow {
  id: string
  meta: HarnessMeta
  capability: Capability | null
  bundles: Bundle[]
  accounts: HarnessAccount[]
  activeAccount: HarnessAccount | null
  needsInstall: boolean
  installing: boolean
  recommendation: RecommendedVersion | null
  version: string
  sharedPersonalLogin: boolean
  switchPending: boolean
  runningTurns: number
  needsSignIn: boolean
  update: string
  status: string
  reason: string
}
export function harnessRowState(harnessId: string, snapshot: Partial<HarnessSnapshot> | null | undefined): HarnessRow {
  const id = harnessId
  const cap = (snapshot?.capabilities ?? []).find((c) => c.harness === id) ?? null
  const bundles = (snapshot?.bundles ?? []).filter((b) => b.harness === id)
  const accounts = (snapshot?.accounts ?? []).filter((a) => a.harness === id)
  const activeAccount = accounts.find((a) => a.active) ?? null
  const doctors = (snapshot?.profiles ?? []).filter((d) => d.harness === id)
  const pendingDoc = doctors.find((d) => d.switchPending || d.status === 'switch pending')
  const dynaDoc: Doctor | undefined = doctors[0]
  const row: Omit<HarnessRow, 'status' | 'reason'> = {
    id, meta: harnessMeta(id), capability: cap, bundles, accounts, activeAccount,
    needsInstall: bundles.length === 0,
    installing: (snapshot?.installing ?? []).some((h) => h === id),
    recommendation: (snapshot?.recommendedVersions ?? []).find((item) => item.harness === id) ?? null,
    version: bundles.find((bundle) => bundle.version === snapshot?.selectedVersions?.[id])?.version || bundles[0]?.version || dynaDoc?.version || '',
    // Explicit backend capabilities take precedence, including false. The list
    // preserves warnings for older snapshots that omit this capability.
    sharedPersonalLogin: !!(cap?.sharedPersonalLogin ?? ['antigravity', 'muse'].includes(id)),
    switchPending: !!pendingDoc, runningTurns: pendingDoc?.runningTurns ?? 0,
    needsSignIn: hasSubscriptionLogin(id) && (!activeAccount || accountNeedsSignIn(activeAccount) || doctors.some((d) => d.status === 'not signed in')),
    update: '',
  }
  // update is the recommended version when it is newer than Dyna's copy.
  const latest = row.needsInstall ? '' : recommendedVersion(row)
  row.update = latest && newer(latest, row.version) ? latest : ''
  if (pendingDoc) {
    return { ...row, needsSignIn: false, status: 'switch pending', reason: pendingDoc.reason || `switch pending: ${row.runningTurns} turns running` }
  }
  if (row.installing) {
    return { ...row, needsSignIn: false, status: 'installing', reason: `Dyna is installing its copy of ${row.meta.name}. This can take a minute.` }
  }
  if (row.needsInstall) {
    return { ...row, needsSignIn: false, status: 'not installed', reason: 'Dyna installs its private copy when you sign in or a session needs it.' }
  }
  const unavailableDoc = doctors.find((d) => d.status === 'unavailable')
  if (unavailableDoc) {
    return { ...row, needsSignIn: false, status: 'unavailable', reason: unavailableDoc.reason || 'The status check did not finish. Refresh to try again.' }
  }
  const blockedDoc = doctors.find((d) => d.status === 'blocked')
  if (cap?.accountBlocked || blockedDoc) {
    return { ...row, needsSignIn: !cap?.accountBlocked && row.needsSignIn, status: 'blocked', reason: cap?.accountBlocked || blockedDoc?.reason || 'Install a private copy to use this account.' }
  }
  if (row.needsSignIn) {
    return { ...row, status: 'not signed in', reason: dynaDoc?.reason || 'Sign in and activate an account for this harness.' }
  }
  const partial = cap?.status === 'partial' || dynaDoc?.status === 'partial' || !!cap?.reason
  return { ...row, status: partial ? 'partial' : 'ready', reason: cap?.reason || dynaDoc?.reason || 'All settings are private to Dyna.' }
}

// rowLine is a harness row's second line: who is signed in, then Dyna's version.
export function rowLine(row: Pick<HarnessRow, 'id' | 'installing' | 'needsInstall' | 'version' | 'switchPending' | 'activeAccount' | 'needsSignIn' | 'meta'>): string {
  if (row.installing) return 'Installing…'
  if (row.needsInstall) return 'Not installed'
  if (!hasSubscriptionLogin(row.id)) return row.version
  const who = row.switchPending ? 'Switching account…' : row.activeAccount && !row.needsSignIn ? row.activeAccount.label || row.meta.name : 'Not signed in'
  return `${who} · ${row.version}`
}

export function toolEnabledForHarness(entry: { enabledHarnesses?: string[] } | null | undefined, harnessId: string): boolean {
  return (entry?.enabledHarnesses ?? []).includes(harnessId)
}

// HarnessTab is one of the two views of the harness settings pane.
export type HarnessTab = 'harnesses' | 'tools'

// nextTab is the view that a tablist key moves to, or null for any other key.
export function nextTab(current: string, key: string): HarnessTab | null {
  if (key === 'Home') return 'harnesses'
  if (key === 'End') return 'tools'
  if (key !== 'ArrowLeft' && key !== 'ArrowRight') return null
  return current === 'harnesses' ? 'tools' : 'harnesses'
}

export function toolSummary(kind: string, entry: { url?: string; command?: string; args?: string[]; source?: string } | null | undefined): string {
  if (kind === 'mcp') {
    if (entry?.url) return entry.url
    return [entry?.command, ...(entry?.args ?? [])].filter(Boolean).join(' ') || 'Dyna MCP server'
  }
  return entry?.source || 'Dyna skill'
}
