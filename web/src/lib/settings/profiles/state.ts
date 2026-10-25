import type { Catalog, CatalogModel, Compact, CompactRule, Effort, UsageAccount } from '../../api/account.ts'
import type { ProfileView } from '../../api/profile.ts'

export interface HarnessChoice {
  id: string
  label: string
  icon: string
  description: string
}
export const harnesses: HarnessChoice[] = [
  { id: 'opencode', label: 'OpenCode', icon: 'opencode', description: 'One harness, many providers. Browse Go, free, and API models.' },
  { id: 'codex', label: 'Codex', icon: 'codex', description: 'OpenAI’s coding agent. Use your ChatGPT plan or an API key.' },
  { id: 'claude-code', label: 'Claude Code', icon: 'claude', description: 'Anthropic’s coding agent, with Claude models and familiar controls.' },
  { id: 'muse', label: 'Muse', icon: 'muse', description: 'A dedicated harness for Muse models and agent sessions.' },
  { id: 'pi', label: 'Pi', icon: 'pi', description: 'A minimal agent harness. Sign in to Anthropic, OpenAI or Copilot inside Pi, or use API keys.' },
]
export function inCategory(model: Pick<CatalogModel, 'category'>, category: string): boolean {
  return category === 'all' || (category === 'opencode' ? ['go', 'free'].includes(model.category) : model.category === category)
}
export function filterModels(models: CatalogModel[], { category = 'all', provider = '', search = '' }: { category?: string; provider?: string; search?: string } = {}): CatalogModel[] {
  const query = search.trim().toLocaleLowerCase()
  return models.filter(m => inCategory(m, category) && (!provider || m.providerId === provider || m.publisher === provider) &&
    [m.id, m.label, m.providerId, m.publisher].join(' ').toLocaleLowerCase().includes(query))
}
// Never substitute a similar model for the exact route requested by Usage.
export function routeModel(models: readonly CatalogModel[] | null | undefined, routeId?: string): CatalogModel | null {
  return routeId ? (models ?? []).find(m => m.id === routeId) ?? null : null
}
export function selectedAccount(accounts: readonly UsageAccount[], harness: string, model: Pick<CatalogModel, 'id' | 'integrationId' | 'providerId'> | null | undefined, connectionId = ''): UsageAccount | null {
  const candidates = accounts.filter(a => a.harness === harness)
  // Provider identifies model billing; integration identifies its credentials.
  const integration = model?.integrationId || model?.providerId
  const matchesRoute = (a: UsageAccount) => (harness !== 'opencode' || !integration || a.providerId === integration) && (!a.routeId || a.routeId === model?.id)
  if (connectionId) return candidates.find(a => a.id === connectionId && a.active && matchesRoute(a)) ?? null
  const active = candidates.filter(a => a.active && matchesRoute(a))
  return active.length === 1 ? active[0] ?? null : null
}
// Trim, drop empty IDs and remove duplicates. Order stays the same.
const cleanIds = (list: unknown): string[] => [...new Set((Array.isArray(list) ? list : []).filter((x): x is string => typeof x === 'string').map(x => x.trim()).filter(Boolean))]
export function normalizeModels(model = '', models: readonly unknown[] = []): string[] {
  const list = cleanIds(models), solo = cleanIds([model])
  if (list.length === 1 && solo.length === 1 && solo[0] !== list[0]) return solo
  return list.length ? list : solo
}
// Who may use a profile (internal/profile Uses), each one a switch: workflow
// agent() calls, subagent sessions, and services (Dyna's PR reviewers).
// The engine sends every allowed use.
export const USES: [string, string][] = [['workflows', 'Workflows'], ['agents', 'Agents'], ['services', 'Services']]
// toggleUse turns one use on or off and keeps the display order.
export const toggleUse = (uses: readonly string[] | null | undefined, use: string, on: boolean): string[] => USES.map(([u]) => u).filter((u) => (u === use ? on : (uses ?? []).includes(u)))
// A profile as the editor holds it: every list is an array and every field is set.
export type ProfileDraft = Omit<ProfileView, 'runtimeVersion' | 'fallback' | 'support' | 'models' | 'uses' | 'extraArgs' | 'command' | 'envKeys'> & {
  models: string[]
  uses: string[]
  extraArgs: string[]
  command: string[]
  envKeys: string[]
}
export function blankProfile(harness = 'opencode', model = '', models: readonly unknown[] | undefined = undefined): ProfileDraft {
  const chain = normalizeModels(model, models)
  return { name: '', description: '', harness, model: chain[0] ?? '', models: chain, taste: 5, diagnostics: 5, intelligence: 5, cost: 5, timeoutSec: 0, maxConcurrent: 0, maxCallsPerRun: 0, default: false, disabled: false, uses: USES.map(([u]) => u), safeMode: false, extraArgs: [], compact: null, command: [], envKeys: [] }
}
// A catalog effort carries the exact extraArgs that select it on its harness.
const indexOfArgs = (args: readonly string[], part: readonly string[]): number => part.length ? args.findIndex((_, i) => part.every((x, j) => args[i + j] === x)) : -1
// effortIn names the effort whose args already appear in args, or ''.
export function effortIn(args: readonly string[] = [], efforts: readonly Effort[] | null = []): string {
  return (efforts ?? []).find(e => indexOfArgs(args, e.args ?? []) >= 0)?.id ?? ''
}
// routeEffort finds the catalog model and effort whose model ID is routeId,
// for harnesses that encode the effort in the model ID.
export function routeEffort(models: readonly CatalogModel[] | null | undefined, routeId?: string): { model: CatalogModel; effort: string } | null {
  for (const m of models ?? []) {
    const e = routeId ? (m.efforts ?? []).find(e => e.model === routeId) : null
    if (e) return { model: m, effort: e.id }
  }
  return null
}
// withEffort removes the args of every known effort, then adds the chosen one's.
// Pass the whole catalog's efforts so a change of model drops the old choice.
export function withEffort(args: readonly string[] = [], known: readonly Effort[] | null | undefined = [], chosen: Effort | null = null): string[] {
  const out = [...args]
  for (const e of known ?? []) {
    const part = e.args ?? []
    for (let i = indexOfArgs(out, part); i >= 0; i = indexOfArgs(out, part)) out.splice(i, part.length)
  }
  return [...out, ...(chosen?.args ?? [])]
}
// profileEffort finds the catalog model and effort that a profile's model ID
// and args select. A model-ID effort selects its group model.
export function profileEffort(models: readonly CatalogModel[] | null | undefined, modelId: string, args: readonly string[] = []): { model: CatalogModel | null; effort: string } {
  const exact = routeModel(models, modelId)
  if (exact) return { model: exact, effort: effortIn(args, exact.efforts) }
  return routeEffort(models, modelId) ?? { model: null, effort: '' }
}
// chooseEffort returns the model ID and args after choosing effort id ('' for
// the harness default) on catalog model entry.
export function chooseEffort(entry: Pick<CatalogModel, 'id' | 'efforts'>, id: string, modelId: string, args: readonly string[] = []): { model: string; args: string[] } {
  const chosen = (entry.efforts ?? []).find(e => e.id === id) ?? null
  if ((entry.efforts ?? []).some(e => e.model)) return { model: chosen?.model || entry.id, args: [...args] }
  return { model: modelId, args: withEffort(args, entry.efforts, chosen) }
}
// compactWindows maps each model of the chain to its catalog context window,
// or to the saved window when the catalog does not list it (0 when unknown).
// A fallback model gets its own window.
export function compactWindows(models: readonly CatalogModel[] | null | undefined, chain: readonly string[], current: Compact | null = null): Record<string, number> {
  const ids = chain.length ? chain : ['']
  return Object.fromEntries(ids.map(id => [id, profileEffort(models, id).model?.context || current?.windows?.[id] || 0]))
}
// A catalog compact rule says what the harness takes for the profile's
// compact field. compactBlock names why the field is off, or ''.
export function compactBlock(rule: CompactRule | null | undefined, windows: Record<string, number> = {}): string {
  if (rule?.reason) return rule.reason
  const missing = Object.keys(windows).filter(id => !((windows[id] ?? 0) > 0))
  if (rule?.window && missing.length) return `The harness counts compaction against the context window, and the catalog reports none for ${missing.map(id => id || 'the harness default model').join(', ')}.`
  return ''
}
// compactFor builds the compact field for tokens, or null for the harness
// default. Relative harnesses also store every chain model's window.
export function compactFor(rule: CompactRule | null | undefined, windows: Record<string, number>, tokens: number): Compact | null {
  if (!(tokens > 0) || !rule || compactBlock(rule, windows)) return null
  return rule.window ? { tokens, windows: { ...windows } } : { tokens }
}
// compactHint explains the compact field: why it is off, or its range and,
// for trigger harnesses, that Dyna sends the harness's compact command.
export function compactHint(rule: CompactRule | null | undefined, off = '', max = 0): string {
  if (off) return off
  const range = (rule?.min ?? 0) > 1 || max ? ` From ${(rule?.min || 1).toLocaleString()}${max ? ` to ${max.toLocaleString()}` : ''}.` : ''
  const trigger = rule?.trigger ? ' This harness has no threshold setting, so Dyna sends its compact command before the next turn once a model call’s prompt reaches this size.' : ''
  return `Leave blank for the harness default.${range}${trigger}`
}
const ACCESS: Record<string, string> = { go: 'OpenCode Go', free: 'Free', other: 'Provider access' }
export const accessLabel = (model?: Pick<CatalogModel, 'category'> | null): string => ACCESS[model?.category ?? ''] ?? 'Manual entry'

const PUBLISHER_ICONS: Record<string, string> = { openai: 'codex', anthropic: 'claude', muse: 'muse', xai: 'grok', opencode: 'opencode', 'opencode-go': 'opencode', 'managed:kimi-code': 'kimi-code', kimi: 'kimi-code', moonshot: 'kimi-code' }
export function publisherIcon(model?: Pick<CatalogModel, 'publisher' | 'providerId'> | null): string {
  const publisher = (model?.publisher || model?.providerId || '').toLowerCase()
  return PUBLISHER_ICONS[publisher] || ''
}

// Where the new-profile wizard starts: a harness, a model route and the account to use.
export interface ModelSelection {
  harness?: string
  routeId?: string
  connectionId?: string
}
// A profile as the editor takes it: a saved one, or a draft that has no runtime or support yet.
export type EditableProfile = Omit<ProfileView, 'runtimeVersion' | 'fallback' | 'support'> & Partial<Pick<ProfileView, 'runtimeVersion' | 'fallback' | 'support'>>
// What the editor keeps in memory while the wizard changes steps: the profile and the raw text and write-only values.
export interface EditorDraft {
  profile: EditableProfile
  models: string[]
  args: string
  command: string
  envSet: Record<string, string>
  envRemove: string[]
  newKey: string
  newVal: string
  replacing: string
  replacementValue: string
  advancedOpen: boolean
}
// The model the wizard chose, as the editor's summary shows it.
export interface ModelSummary {
  label?: string
  modelId: string
  harness: string
  harnessLabel: string
  publisher?: string
  icon: string
}
