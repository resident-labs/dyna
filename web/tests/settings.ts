// Browser fixture only. This entry is not imported by the production app.
// Open tests/settings.html?pane=skills or ?pane=mcp to see a pane with sample data.
import '@fontsource/ibm-plex-sans/400.css'
import '@fontsource/ibm-plex-sans/500.css'
import '@fontsource/ibm-plex-sans/600.css'
import '@fontsource/ibm-plex-sans/700.css'
import '@fontsource/jetbrains-mono/400.css'
import '@fontsource/jetbrains-mono/500.css'
import '../src/lib/ui/tokens.css'
import '../src/app.css'
import { mount } from 'svelte'
import { api } from '../src/api.ts'
import Settings from '../src/lib/settings/Settings.svelte'
import { fixtureWindow, rollupFixture } from './usageRollupFixture.ts'
import { account } from '../test/account-fixtures.ts'
import { need } from '../test/need.ts'
import type { Capability, HarnessAccount, HarnessConfig, HarnessSnapshot, Bundle, ManagedMCP, ManagedSkill } from '../src/lib/api/harness.ts'
import type { HarnessSupport } from '../src/lib/settings/support.ts'
import type { ProfileList, ProfileUsageStats, ProfileView } from '../src/lib/api/profile.ts'
import type { ProfileSave } from '../src/lib/api/engine.ts'
import type { ScopeAccount } from '../src/lib/api/account.ts'
import type { ActivityRollup } from '../src/lib/settings/accounts/activity.ts'
import type { Device, Hook, HookSummary, SettingItem } from '../src/lib/api/settings.ts'

const query = new URLSearchParams(location.search)
const pane = query.get('pane') || 'harnesses'
// ?empty=1 shows every list with no rows. ?delay=<ms> slows every api call, to see loading states.
const empty = query.has('empty')
const delay = Number(query.get('delay') || 120)
const caps: Capability[] = ['claude-code', 'pi', 'mock'].map((harness) => ({ harness, status: 'ready', reason: '', installSupported: true, accountBlocked: '', mcp: true, concurrent: false, sharedPersonalLogin: false }))
const bundle = (harness: string, version: string): Bundle => ({ harness, version, executable: '', directory: '' })
const harnessAccount = (id: string, harness: string, label: string, active: boolean): HarnessAccount => ({ id, harness, label, home: '', identity: '', status: 'ready', active, repos: null })
// The runtime snapshot the fixture changes in place: its lists are never null.
interface Snapshot extends HarnessSnapshot {
  bundles: Bundle[]
  accounts: HarnessAccount[]
}
const selectedVersions: Record<string, string> = { 'claude-code': '2.1.302', pi: '1.3.0' }
const snapshot: Snapshot = {
  bundles: [bundle('claude-code', '2.1.302'), bundle('pi', '1.3.0')],
  accounts: [
    harnessAccount('claude-3', 'claude-code', 'Work', true),
    harnessAccount('claude-p', 'claude-code', 'Personal', false),
    harnessAccount('pi-1', 'pi', 'OpenCode Go', true),
    harnessAccount('pi-2', 'pi', 'user@example.com', false),
  ],
  profiles: [],
  capabilities: caps.map((c) => c.harness === 'pi' ? { ...c, status: 'partial', reason: 'No switch verified for native memory.' } : c),
  selectedVersions,
  recommendedVersions: [{ harness: 'claude-code', version: '2.1.310', status: 'resolved', source: 'npm' }],
  installing: [],
}
const catalog: HarnessConfig = {
  mcp: empty ? [] : [
    { name: 'github', command: 'gh-mcp', enabledHarnesses: ['claude-code', 'pi'] },
    { name: 'browser', url: 'https://mcp.example.com/browser', enabledHarnesses: ['pi'] },
  ],
  skills: empty ? [] : [
    { name: 'release-notes', source: '/home/user/.dyna/skills/release-notes', enabled: true },
    { name: 'code-review', source: '/home/user/.dyna/skills/code-review', enabled: false },
  ],
}
const later = <T>(value: T, ms = delay): Promise<T> => new Promise((resolve) => setTimeout(() => resolve(structuredClone(value)), ms))
api.privateRuntimes = () => later(snapshot)
api.harnessConfig = () => later(catalog)
api.addHarnessSkill = (entry: ManagedSkill) => { catalog.skills = [...catalog.skills, entry]; return later(catalog) }
api.addHarnessMCP = (entry: ManagedMCP) => { catalog.mcp = [...catalog.mcp, entry]; return later(catalog) }
api.setHarnessConfigEnabled = (kind: string, name: string, harness: string, enabled: boolean) => {
  if (kind === 'skill') {
    const skill = need(catalog.skills.find((s) => s.name === name))
    skill.enabled = enabled
  } else {
    const server = need(catalog.mcp.find((m) => m.name === name))
    server.enabledHarnesses = enabled ? [...new Set([...server.enabledHarnesses, harness])] : server.enabledHarnesses.filter((h) => h !== harness)
  }
  return later(catalog)
}
api.removeHarnessConfig = (kind: string, name: string) => {
  if (kind === 'skill') catalog.skills = catalog.skills.filter((s) => s.name !== name)
  else catalog.mcp = catalog.mcp.filter((m) => m.name !== name)
  return later(catalog)
}
api.privateRuntimeLogins = () => later([])
api.installPrivateRuntime = (harness: string, version: string) => {
  snapshot.bundles = [...snapshot.bundles.filter((b) => b.harness !== harness), bundle(harness, version)]
  selectedVersions[harness] = version
  return later(snapshot, 2500)
}
api.uninstallPrivateRuntime = (harness: string) => {
  snapshot.bundles = snapshot.bundles.filter((b) => b.harness !== harness)
  delete selectedVersions[harness]
  return later(snapshot)
}
api.beginPrivateRuntimeLogin = () => new Promise(() => {})

const minutes = (n: number) => new Date(Date.now() - n * 60000).toISOString()
const inHours = (n: number) => new Date(Date.now() + n * 3600000).toISOString()
const supportItem = (label: string, state: string, chip: string, reason: string) => ({ key: label, label, state, chip, reason })
const support = (fields: Partial<HarnessSupport> = {}): HarnessSupport => ({ installed: true, installable: true, binary: '', version: '', versionError: '', lastHealthyVersion: '', dialect: '', partial: false, status: 'healthy', reason: '', items: [], ...fields })
const profile = (name: string, harness: string, model: string, extra: Partial<ProfileView> = {}): ProfileView => ({ name, harness, model, models: [model], envKeys: [], uses: ['workflows', 'agents', 'services'], taste: 7, diagnostics: 7, intelligence: 7, cost: 7, description: '', support: support(), runtimeVersion: '', fallback: '', timeoutSec: 0, maxConcurrent: 0, maxCallsPerRun: 0, default: false, disabled: false, safeMode: false, extraArgs: [], compact: null, ...extra })
// The profile list the fixture changes in place: its profiles are never null.
interface Listed extends ProfileList {
  profiles: ProfileView[]
}
const profileList: Listed = {
  default: 'sol',
  harnesses: ['claude-code', 'pi', 'mock', 'custom'],
  unready: {},
  profiles: [
    profile('sol', 'pi', 'openai-codex/gpt-6.1-sol', { taste: 7, intelligence: 9, cost: 8, description: 'A work-horse and thorough debugger for tasks that need intelligence.' }),
    profile('opus', 'claude-code', 'opus', { taste: 10, intelligence: 10, cost: 4, description: 'Highest taste. Use for design and review.', envKeys: ['ANTHROPIC_BASE_URL'] }),
    profile('mimo', 'pi', 'opencode-go/mimo-v2.6-pro', { taste: 7, intelligence: 8, cost: 10, models: ['opencode-go/mimo-v2.6-pro', 'opencode-go/glm-5'], description: 'A cheap work-horse for routine implementation.' }),
    profile('haiku', 'claude-code', 'haiku', { taste: 6, intelligence: 7, cost: 9, uses: ['agents'], description: 'Fast mechanical edits from a rigid specification.' }),
    profile('wonder-full', 'pi', 'muse-wonder-full-1.0', { taste: 10, intelligence: 8, cost: 4, support: support({ status: 'unverified_since_update', version: '1.4.10', items: [supportItem('Token usage', 'no', 'no usage', 'The harness does not print token counts.')] }) }),
    profile('scratch', 'custom', '', { taste: 5, intelligence: 5, cost: 5, disabled: true }),
  ],
}
api.configProfiles = () => later(empty ? { ...profileList, profiles: [], default: '' } : profileList)
const profileActivity: ProfileUsageStats[] = [
  { profile: 'sol', turns: 42, medianWallMs: 183_000, cost: { usd: 12.40, completeTurns: 40, partialTurns: 1, unknownTurns: 1 }, costPerTurnUsd: 12.40 / 41, cacheReadShare: 0.684, cachePaired: { input: 1_000_000, cached: 684_000, turns: 39 } },
  { profile: 'mimo', turns: 95, medianWallMs: 45_000, cost: { usd: 8.75, completeTurns: 94, partialTurns: 0, unknownTurns: 1 }, costPerTurnUsd: 8.75 / 94, cacheReadShare: 0.72, cachePaired: { input: 2_000_000, cached: 1_440_000, turns: 95 } },
  { profile: 'wonder-full', turns: 11, medianWallMs: 62_000, cost: { usd: null, completeTurns: 0, partialTurns: 0, unknownTurns: 11 }, costPerTurnUsd: null, cacheReadShare: null, cachePaired: { input: 0, cached: 0, turns: 0 } },
]
api.profileUsage = (from: string, to: string) => later({ from: from.slice(0, 10), to: to.slice(0, 10), profiles: empty ? [] : profileActivity })
api.setDefaultProfile = (name: string) => { profileList.default = name; return later(null) }
api.setProfileDisabled = (name: string, disabled: boolean) => { profileList.profiles = profileList.profiles.map((p) => p.name === name ? { ...p, disabled } : p); return later(null) }
api.saveProfile = ({ original, profile: p }: ProfileSave) => { profileList.profiles = profileList.profiles.map((x) => x.name === original ? { ...x, ...p } : x); return later(need(profileList.profiles.find((x) => x.name === p.name))) }
api.removeProfile = (name: string) => { profileList.profiles = profileList.profiles.filter((p) => p.name !== name); return later(null) }

const window5 = (id: string, label: string, used: number) => fixtureWindow(id, label, used, inHours(id === 'short' ? 3 : 70))
const savedAccounts: ScopeAccount[] = [
  { ...account({ id: 'claude-work', harness: 'pi', provider: 'anthropic', kind: 'oauth', label: 'Claude · Work', active: true, plan: { id: 'max', label: 'Max 20x' }, source: 'Claude account report', observedAt: minutes(2), profiles: ['opus', 'haiku'], windows: [window5('short', '5-hour window', 32), window5('weekly', 'Weekly window', 59)] }), activeHere: true },
  { ...account({ id: 'claude-home', harness: 'pi', provider: 'anthropic', kind: 'oauth', label: 'Claude · Personal', source: 'Claude account report', observedAt: minutes(4), windows: [window5('short', '5-hour window', 11), window5('weekly', 'Weekly window', 22)] }), activeHere: false },
  { ...account({ id: 'chatgpt-1', harness: 'pi', provider: 'chatgpt', kind: 'oauth', label: 'ChatGPT account', active: true, plan: { id: 'plus', label: 'Plus' }, source: 'ChatGPT account report', observedAt: minutes(5), profiles: ['sol'], windows: [window5('short', '5-hour window', 18), window5('weekly', 'Weekly window', 41)] }), activeHere: true },
  { ...account({ id: 'go', harness: 'pi', provider: 'opencode-go', kind: 'api_key', label: 'OpenCode Go · Personal', active: true, source: 'OpenCode Console usage', observedAt: minutes(9), profiles: ['mimo'], windows: [{ ...window5('short', '5-hour window', 20), used: 2.4, limit: 12, unit: 'USD' }, { ...window5('weekly', 'Weekly window', 30), used: 9, limit: 30, unit: 'USD' }] }), activeHere: true },
]
const accountsSnapshot = (scope = '') => {
  const providers = [...new Set(savedAccounts.map((a) => a.provider ?? ''))]
  return { scope, providers: empty ? [] : providers.map((provider) => ({ provider, accounts: savedAccounts.filter((a) => (a.provider ?? '') === provider) })), problems: [], checkedAt: minutes(1) }
}
api.accountsView = (scope = '') => later(accountsSnapshot(scope))
api.accountsList = (scope = '') => later(accountsSnapshot(scope))
api.accountsUse = (id: string, scope = '') => {
  const chosen = savedAccounts.find((a) => a.id === id)
  if (chosen) for (const a of savedAccounts) if (a.provider === chosen.provider) a.activeHere = a.id === id
  return later(accountsSnapshot(scope))
}
api.accountsAdd = () => later(accountsSnapshot())
api.setAccountRepos = () => later(snapshot)
api.accountsRemove = (id: string) => {
  const at = savedAccounts.findIndex((a) => a.id === id)
  if (at >= 0) savedAccounts.splice(at, 1)
  return later(accountsSnapshot())
}
const none = { tokens: null, completeTurns: 0, partialTurns: 0, unknownTurns: 0 }
const emptyRollup: ActivityRollup = { from: '', to: '', totals: { turns: 0, wallMs: 0, input: none, cached: none, output: none, cost: { usd: null, completeTurns: 0, partialTurns: 0, unknownTurns: 0 }, cachePaired: { input: 0, cached: 0, turns: 0 } }, byDayModel: [], byRoleModel: [] }
api.usageRollup = (_room: string | undefined, from: string, to: string) => later(empty ? emptyRollup : rollupFixture(Math.max(1, Math.round((Date.parse(to) - Date.parse(from)) / 864e5) + 1)))
api.rooms = () => later([{ id: 'room-a', name: 'dyna-agent', lastActivity: minutes(3), unread: 0, running: 0, members: 2, pinned: false, pinOrder: 0, archived: false }])

const item = (fields: Pick<SettingItem, 'key' | 'kind' | 'value' | 'source' | 'help'> & Partial<SettingItem>): SettingItem => ({ liveVsRestart: '', ...fields })
const settingItems: SettingItem[] = [
  item({ key: 'browser.path', kind: 'string', value: '/usr/bin/chromium', source: 'file', help: 'The Chromium or Chrome binary that computers launch.' }),
  item({ key: 'browser.extraFlags', kind: 'string', value: '', source: 'default', help: 'Extra command-line flags for every computer browser.' }),
  item({ key: 'browser.useFlagsFile', kind: 'bool', value: true, source: 'default', help: 'Also read flags from your chromium-flags.conf.' }),
  item({ key: 'browser.profileMode', kind: 'enum', value: 'shared', options: ['shared', 'fresh'], source: 'default', help: 'Whether computers share one browser profile or start fresh.' }),
  item({ key: 'browser.saveBack', kind: 'bool', value: false, source: 'default', help: 'Copy cookies back to the shared profile when a computer stops.' }),
  item({ key: 'content.maxCardBytes', kind: 'bytes', value: 1048576, source: 'default', help: 'The largest HTML or JavaScript card a session can post.' }),
  item({ key: 'content.maxFileBytes', kind: 'bytes', value: 26214400, source: 'file', help: 'The largest file attachment a session can post.' }),
  item({ key: 'root.profile', kind: 'select', value: 'opus', options: ['opus', 'sol', 'mimo'], source: 'file', help: 'The profile a new root session uses.' }),
  item({ key: 'library.profile', kind: 'select', value: '', options: ['', 'sol', 'mimo'], optionLabels: ['Auto · best taste + cost (sol)', 'sol · thrift 8', 'mimo · thrift 10'], source: 'default', help: 'One profile for all librarian work. Auto ranks enabled service profiles.' }),
  item({ key: 'voice.provider', kind: 'enum', value: 'openai', options: ['openai', 'google', 'doubao'], source: 'file', help: 'The realtime provider for Voice calls.' }),
  item({ key: 'subagent.maxDepth', kind: 'int', value: 3, source: 'default', help: 'How deep sessions can start child sessions.' }),
  item({ key: 'sessions.checkInInterval', kind: 'duration', value: '15m', source: 'default', help: 'How often parents review their children.' }),
  item({ key: 'steering.maxToolWait', kind: 'duration', value: '30s', source: 'default', help: 'How long a busy tool can run before a message interrupts it.' }),
  item({ key: 'worktrees.retention', kind: 'duration', value: '72h', source: 'default', help: 'How long Dyna keeps finished workflow worktrees.' }),
  item({ key: 'updater.autoCheck', kind: 'bool', value: true, source: 'default', help: 'Check for a new Dyna version in the background.' }),
]
api.settings = () => later(empty ? [] : settingItems)
api.listServices = () => later([{ role: 'reviewer', name: 'PR reviewers', page: 'services', profile: 'sol', pinned: '', source: 'auto', reason: '', scope: 'room', sessions: 0, profiles: ['sol', 'mimo'] }])
api.setServiceProfile = () => later(null)
api.setSetting = (key: string, value: string) => {
  const it = need(settingItems.find((x) => x.key === key))
  it.value = it.kind === 'bool' ? value === 'true' : it.kind === 'int' ? Number(value) : value
  it.source = 'file'
  return later(it)
}

const hook = (fields: Pick<Hook, 'name' | 'event'> & Partial<Hook>): Hook => ({ label: '', command: '', cwd: 'data', timeout: '5m0s', onFail: 'alert', scope: 'global', addedBy: 'dyna', addedAt: '', ...fields })
const hookList: Hook[] = [
  hook({ name: 'self-update', label: 'Update Dyna', event: 'main-updated', builtin: 'self-update' }),
  hook({ name: 'maintenance', label: 'Maintenance', event: 'schedule', everyMinutes: 60, builtin: 'maintenance' }),
  hook({ name: 'doctor', label: 'Doctor', event: 'schedule', everyMinutes: 1, builtin: 'doctor' }),
  hook({ name: 'notify-me', event: 'after-land', command: 'notify-send landed', addedBy: 'user' }),
]
api.listGlobalHooks = () => later(hookList)
api.globalHookSummary = () => later<HookSummary[]>([])

const device = (fields: Pick<Device, 'kind' | 'id' | 'name' | 'device' | 'origin' | 'lastSeen' | 'expires'> & Partial<Device>): Device => ({ current: false, created: '', ...fields })
const deviceList: Device[] = [
  device({ kind: 'paired', id: 'd1', name: 'Pixel 9', device: 'Chrome on Android', origin: 'http://192.168.1.20:7373', lastSeen: minutes(4), expires: inHours(24 * 20), legacy: true, scopeLabel: 'legacy: full access', scopes: ['read', 'operate', 'files', 'terminal', 'computers', 'admin'] }),
  device({ kind: 'paired', id: 'd2', name: 'Work laptop', device: 'Firefox on Linux', origin: 'http://192.168.1.31:7373', lastSeen: minutes(300), expires: inHours(24 * 3), current: true }),
  device({ kind: 'paired', id: 'd3', name: 'iPad', device: 'Safari on iPadOS', origin: 'https://dyna.example.net', lastSeen: '0001-01-01T00:00:00Z', expires: inHours(-2) }),
  device({ kind: 'connect', id: 'c1', name: 'user@example.com', device: 'Chrome on macOS', origin: 'https://connect.residentlabs.dev', lastSeen: minutes(40), expires: inHours(30) }),
]
api.devices = () => later(empty ? [] : deviceList)
api.revokeDevice = (kind: string, id: string) => { const keep = deviceList.filter((d) => d.kind !== kind || (id !== 'all' && d.id !== id)); deviceList.splice(0, deviceList.length, ...keep); return later(1) }
api.setDeviceScopes = (id: string, scopes: string[]) => { const row = need(deviceList.find((d) => d.id === id)); row.scopes = scopes; row.legacy = false; row.scopeLabel = scopes.join(', '); return later(row) }

mount(Settings, { target: need(document.getElementById('app'), 'app'), props: { start: pane, roomId: 'room-1', dataDir: '/home/user/.local/share/dyna', onclose: () => { document.body.dataset.closed = 'yes' } } })
