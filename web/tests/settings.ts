// Browser fixture only. This entry is not imported by the production app.
// Open tests/settings.html?pane=harnesses to see a pane with sample data.
import '@fontsource/ibm-plex-sans/400.css'
import '@fontsource/ibm-plex-sans/500.css'
import '@fontsource/ibm-plex-sans/600.css'
import '@fontsource/ibm-plex-sans/700.css'
import '@fontsource/jetbrains-mono/400.css'
import '@fontsource/jetbrains-mono/500.css'
import '../src/lib/ui/tokens.css'
import '../src/app.css'
import { mount } from 'svelte'
import { api } from '../src/api.js'
import Settings from '../src/lib/settings/Settings.svelte'
import { fixtureWindow, rollupFixture } from './usageRollupFixture.ts'
import { usageAccount } from '../test/account-fixtures.ts'
import { need } from '../test/need.ts'
import type { Capability, HarnessAccount, HarnessConfig, HarnessSnapshot, Bundle } from '../src/lib/api/harness.ts'
import type { HarnessSupport } from '../src/lib/settings/support.ts'
import type { ProfileList, ProfileView } from '../src/lib/api/profile.ts'
import type { UsageAccount } from '../src/lib/api/account.ts'
import type { ActivityRollup } from '../src/lib/settings/usage/activity.ts'
import type { Device, Hook, HookRun, HookRunLog, SettingItem } from '../src/lib/api/settings.ts'

const query = new URLSearchParams(location.search)
const pane = query.get('pane') || 'harnesses'
// ?empty=1 shows every list with no rows. ?delay=<ms> slows every api call, to see loading states.
const empty = query.has('empty')
const delay = Number(query.get('delay') || 120)
const caps: Capability[] = ['claude-code', 'codex', 'opencode', 'antigravity', 'grok', 'pi', 'muse'].map((harness) => ({ harness, status: 'ready', reason: '', installSupported: true, accountBlocked: '', mcp: true, skills: true, skillsPinned: false, concurrent: false, sharedPersonalLogin: false }))
const bundle = (harness: string, version: string): Bundle => ({ harness, version, executable: '', directory: '' })
const harnessAccount = (id: string, harness: string, label: string, active: boolean): HarnessAccount => ({ id, harness, label, home: '', identity: '', status: 'ready', active, repos: null })
// The runtime snapshot the fixture changes in place: its lists are never null.
interface Snapshot extends HarnessSnapshot {
  bundles: Bundle[]
  accounts: HarnessAccount[]
}
const selectedVersions: Record<string, string> = { 'claude-code': '2.1.302', codex: '0.163.0', opencode: '2.0.32', antigravity: '1.4.2', grok: '1.0.62', pi: '1.3.0', muse: '1.4.10-R6427.1' }
const snapshot: Snapshot = {
  bundles: [bundle('claude-code', '2.1.302'), bundle('codex', '0.163.0'), bundle('opencode', '2.0.32'), bundle('antigravity', '1.4.2'), bundle('grok', '1.0.62'), bundle('pi', '1.3.0'), bundle('muse', '1.4.10-R6427.1')],
  accounts: [
    harnessAccount('claude-3', 'claude-code', 'Work', true),
    harnessAccount('claude-p', 'claude-code', 'Personal', false),
    harnessAccount('codex-1', 'codex', 'ChatGPT account', true),
    harnessAccount('oc-1', 'opencode', 'OpenCode Go', true),
    harnessAccount('agy-1', 'antigravity', 'Antigravity account', true),
    harnessAccount('grok-1', 'grok', 'Current Grok account', true),
    harnessAccount('muse-1', 'muse', 'user@example.com', true),
  ],
  profiles: [],
  capabilities: caps.map((c) => ['antigravity', 'grok', 'muse'].includes(c.harness) ? { ...c, status: 'partial', reason: 'No switch verified for native memory.' } : c),
  selectedVersions,
  recommendedVersions: [{ harness: 'claude-code', version: '2.1.310', status: 'resolved', source: 'npm' }, { harness: 'codex', version: '0.163.0', status: 'resolved', source: 'npm' }],
  installing: [],
}
const catalog: HarnessConfig = { mcp: [{ name: 'github', command: 'gh-mcp', enabledHarnesses: ['claude-code', 'codex'] }], skills: [{ name: 'release-notes', source: '~/.dyna/skills/release-notes', enabledHarnesses: ['claude-code'] }] }
const later = <T>(value: T, ms = delay): Promise<T> => new Promise((resolve) => setTimeout(() => resolve(structuredClone(value)), ms))
api.privateRuntimes = () => later(snapshot)
api.harnessConfig = () => later(catalog)
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
api.removePrivateRuntimeAccount = (id: string) => {
  snapshot.accounts = snapshot.accounts.filter((a) => a.id !== id)
  return later(snapshot)
}
api.switchPrivateRuntimeAccount = (id: string) => {
  const h = need(snapshot.accounts.find((a) => a.id === id)).harness
  snapshot.accounts = snapshot.accounts.map((a) => a.harness === h ? { ...a, active: a.id === id } : a)
  return later(snapshot)
}
api.findPersonalLogin = () => later(null)
api.beginPrivateRuntimeLogin = () => new Promise(() => {})

const minutes = (n: number) => new Date(Date.now() - n * 60000).toISOString()
const inHours = (n: number) => new Date(Date.now() + n * 3600000).toISOString()
const supportItem = (label: string, state: string, chip: string, reason: string) => ({ key: label, label, state, chip, reason })
const support = (fields: Partial<HarnessSupport> = {}): HarnessSupport => ({ installed: true, installable: true, binary: '', version: '', versionError: '', lastHealthyVersion: '', dialect: '', partial: false, status: 'healthy', reason: '', items: [], ...fields })
const profile = (name: string, harness: string, model: string, extra: Partial<ProfileView> = {}): ProfileView => ({ name, harness, model, models: [model], envKeys: [], uses: ['workflows', 'agents', 'services'], taste: 7, intelligence: 7, cost: 7, description: '', support: support(), runtimeVersion: '', fallback: '', timeoutSec: 0, maxConcurrent: 0, maxCallsPerRun: 0, default: false, disabled: false, safeMode: false, extraArgs: [], compact: null, command: [], ...extra })
// The profile list the fixture changes in place: its profiles are never null.
interface Listed extends ProfileList {
  profiles: ProfileView[]
}
const profileList: Listed = {
  default: 'sol',
  harnesses: ['claude-code', 'codex', 'opencode', 'antigravity', 'muse', 'custom'],
  unready: {},
  profiles: [
    profile('sol', 'codex', 'gpt-6.1-sol', { taste: 7, intelligence: 9, cost: 8, description: 'A work-horse and thorough debugger for tasks that need intelligence.' }),
    profile('opus', 'claude-code', 'opus', { taste: 10, intelligence: 10, cost: 4, description: 'Highest taste. Use for design and review.', envKeys: ['ANTHROPIC_BASE_URL'] }),
    profile('mimo', 'opencode', 'opencode-go/mimo-v2.6-pro', { taste: 7, intelligence: 8, cost: 10, models: ['opencode-go/mimo-v2.6-pro', 'opencode-go/glm-5'], description: 'A cheap work-horse for routine implementation.' }),
    profile('haiku', 'claude-code', 'haiku', { taste: 6, intelligence: 7, cost: 9, uses: ['agents'], description: 'Fast mechanical edits from a rigid specification.' }),
    profile('wonder-full', 'muse', 'muse-wonder-full-1.0', { taste: 10, intelligence: 8, cost: 4, support: support({ status: 'unverified_since_update', version: '1.4.10', items: [supportItem('Token usage', 'no', 'no usage', 'Muse does not print token counts.')] }) }),
    profile('scratch', 'custom', '', { taste: 5, intelligence: 5, cost: 5, disabled: true }),
  ],
}
api.configProfiles = () => later(empty ? { ...profileList, profiles: [], default: '' } : profileList)
api.setDefaultProfile = (name: string) => { profileList.default = name; return later({}) }
api.setProfileDisabled = (name: string, disabled: boolean) => { profileList.profiles = profileList.profiles.map((p) => p.name === name ? { ...p, disabled } : p); return later({}) }
api.saveProfile = ({ original, profile: p }: { original: string; profile: ProfileView }) => { profileList.profiles = profileList.profiles.map((x) => x.name === original ? { ...x, ...p } : x); return later(p) }
api.removeProfile = (name: string) => { profileList.profiles = profileList.profiles.filter((p) => p.name !== name); return later({}) }

const window5 = (id: string, label: string, used: number) => fixtureWindow(id, label, used, inHours(id === 'short' ? 3 : 70))
const usageAccounts: UsageAccount[] = [
  usageAccount({ id: 'claude-work', harness: 'claude-code', label: 'Claude · Work', active: true, plan: { id: 'max', label: 'Max 20x' }, source: 'Claude account report', observedAt: minutes(2), profiles: ['opus', 'haiku'], windows: [window5('short', '5-hour window', 32), window5('weekly', 'Weekly window', 59)] }),
  usageAccount({ id: 'codex-1', harness: 'codex', label: 'Codex · ChatGPT account', active: true, plan: { id: 'plus', label: 'Plus' }, source: 'Codex account report', observedAt: minutes(5), profiles: ['sol'], windows: [window5('short', '5-hour window', 18), window5('weekly', 'Weekly window', 41)] }),
  usageAccount({ id: 'go', harness: 'opencode', label: 'OpenCode Go · Personal', active: true, source: 'OpenCode Console usage', observedAt: minutes(9), profiles: ['mimo'], windows: [{ ...window5('short', '5-hour window', 20), used: 2.4, limit: 12, unit: 'USD' }, { ...window5('weekly', 'Weekly window', 30), used: 9, limit: 30, unit: 'USD' }] }),
]
const usage = () => ({ accounts: empty ? [] : structuredClone(usageAccounts), checkedAt: minutes(1) })
api.accountUsageCached = () => later(usage())
api.accountUsage = () => later(usage())
const none = { tokens: null, completeTurns: 0, partialTurns: 0, unknownTurns: 0 }
const emptyRollup: ActivityRollup = { from: '', to: '', totals: { turns: 0, wallMs: 0, input: none, cached: none, output: none, cost: { usd: null, completeTurns: 0, partialTurns: 0, unknownTurns: 0 }, cachePaired: { input: 0, cached: 0, turns: 0 } }, byDayModel: [], byRoleModel: [] }
api.usageRollup = (_room: string | undefined, from: string, to: string) => later(empty ? emptyRollup : rollupFixture(Math.max(1, Math.round((Date.parse(to) - Date.parse(from)) / 864e5) + 1)))
api.rooms = () => later([{ id: 'room-a', name: 'dyna-agent' }])

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
  item({ key: 'subagent.maxDepth', kind: 'int', value: 3, source: 'default', help: 'How deep sessions can start child sessions.' }),
  item({ key: 'sessions.checkInInterval', kind: 'duration', value: '15m', source: 'default', help: 'How often parents review their children.' }),
  item({ key: 'steering.maxToolWait', kind: 'duration', value: '30s', source: 'default', help: 'How long a busy tool can run before a message interrupts it.' }),
  item({ key: 'worktrees.retention', kind: 'duration', value: '72h', source: 'default', help: 'How long Dyna keeps finished workflow worktrees.' }),
  item({ key: 'updater.autoCheck', kind: 'bool', value: true, source: 'default', help: 'Check for a new Dyna version in the background.' }),
]
api.settings = () => later(empty ? [] : settingItems)
api.setSetting = (key: string, value: string) => {
  const it = need(settingItems.find((x) => x.key === key))
  it.value = it.kind === 'bool' ? value === 'true' : it.kind === 'int' ? Number(value) : value
  it.source = 'file'
  return later(it)
}

const device = (fields: Pick<Device, 'kind' | 'id' | 'name' | 'device' | 'origin' | 'lastSeen' | 'expires'> & Partial<Device>): Device => ({ current: false, created: '', ...fields })
const deviceList: Device[] = [
  device({ kind: 'paired', id: 'd1', name: 'Pixel 9', device: 'Chrome on Android', origin: 'http://192.168.1.20:7373', lastSeen: minutes(4), expires: inHours(24 * 20), legacy: true, scopeLabel: 'legacy: full access', scopes: ['read', 'operate', 'files', 'terminal', 'computers', 'admin'] }),
  device({ kind: 'paired', id: 'd2', name: 'Work laptop', device: 'Firefox on Linux', origin: 'http://192.168.1.31:7373', lastSeen: minutes(300), expires: inHours(24 * 3), current: true }),
  device({ kind: 'paired', id: 'd3', name: 'iPad', device: 'Safari on iPadOS', origin: 'https://dyna.example.net', lastSeen: '0001-01-01T00:00:00Z', expires: inHours(-2) }),
  device({ kind: 'connect', id: 'c1', name: 'user@example.com', device: 'Chrome on macOS', origin: 'https://connect.residentlabs.dev', lastSeen: minutes(40), expires: inHours(30) }),
]
api.devices = () => later(empty ? [] : deviceList)
api.revokeDevice = (kind: string, id: string) => { const keep = deviceList.filter((d) => d.kind !== kind || (id !== 'all' && d.id !== id)); deviceList.splice(0, deviceList.length, ...keep); return later({}) }
api.setDeviceScopes = (id: string, scopes: string[]) => { const row = deviceList.find((d) => d.id === id); if (row) { row.scopes = scopes; row.legacy = false; row.scopeLabel = scopes.join(', ') }; return later({}) }

const hookList: Hook[] = [
  { name: 'lint-web', label: 'Lint web', event: 'before-land', command: 'cd web && npm run lint', cwd: 'worktree', timeout: '5m0s', onFail: 'block', addedBy: 'sam', addedAt: minutes(60 * 24 * 6) },
  { name: 'refresh-main', label: 'Refresh main', event: 'main-updated', command: 'git fetch origin main', cwd: 'repo', timeout: '1m0s', onFail: 'alert', addedBy: 'settings-polish', addedAt: minutes(60 * 5) },
  { name: 'nightly-gc', label: 'Nightly cleanup', event: 'schedule', everyMinutes: 1440, command: 'dyna git worktrees gc', cwd: 'repo', timeout: '10m0s', onFail: 'alert', addedBy: 'sam', addedAt: minutes(60 * 24 * 30) },
]
const hookRun = (id: string, name: string, result: string, started: number, finished: number): HookRun => ({ id, repo: '', name, command: '', logPath: '', result, startedAt: minutes(started), finishedAt: minutes(finished) })
const hookRuns: Record<string, HookRun[]> = { 'lint-web': [hookRun('r1', 'lint-web', 'pass', 25, 24), hookRun('r2', 'lint-web', 'fail', 200, 199)], 'refresh-main': [hookRun('r3', 'refresh-main', 'pass', 12, 12)], 'nightly-gc': [] }
api.hookRepos = () => later(['/home/user/dyna-agent', '/home/user/resident-site'])
api.listHooks = () => later(empty ? [] : hookList)
api.hookRuns = (_repo: string, name: string) => later(hookRuns[name] ?? [])
api.hookRunLog = (_repo: string, _name: string, id: string): Promise<HookRunLog> => later({ tail: id === 'r2' ? 'web/src/App.svelte\n  12:5  error  Unused variable "x"\n' : 'ok\n', exitCode: id === 'r2' ? 1 : 0 })
api.runHook = () => later({ result: 'pass' })
api.addHook = (_repo: string, hook: Omit<Hook, 'addedBy' | 'addedAt'>) => { hookList.unshift({ ...hook, addedBy: 'sam', addedAt: new Date().toISOString() }); return later({}) }
api.editHook = (_repo: string, name: string, patch: Partial<Hook>) => { Object.assign(need(hookList.find((h) => h.name === name)), patch); return later({}) }
api.removeHook = (_repo: string, name: string) => { hookList.splice(hookList.findIndex((h) => h.name === name), 1); return later({}) }

mount(Settings, { target: need(document.getElementById('app'), 'app'), props: { start: pane, dataDir: '/home/user/.local/share/dyna', onclose: () => { document.body.dataset.closed = 'yes' } } })
