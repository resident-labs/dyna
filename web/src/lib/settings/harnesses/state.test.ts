import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import * as harnessState from './state.ts'
import {
  HARNESS_ORDER,
  exactVersion,
  filterProviders,
  harnessMeta,
  harnessRowState,
  hasSubscriptionLogin,
  personalLoginChoice,
  preferenceStatusLabel,
  recommendedVersion,
  recoveredLogins,
  rowLine,
  takesProviderKeys,
  toolEnabledForHarness,
  toolSummary,
} from './state.ts'
import { errorText } from '../../errorText.ts'
import { call, runFunctions } from '../../../../test/svelte-source.ts'
import { need } from '../../../../test/need.ts'
import type { HarnessAccount, HarnessSnapshot, PersonalLogin, PrivateLogin, RecommendedVersion } from '../../api/harness.ts'

// Snapshot data that leaves out fields the engine may omit, so the helpers read it as the engine sends it.
const loose = (value: object): Partial<HarnessSnapshot> => JSON.parse(JSON.stringify(value))
const paneSource = readFileSync(new URL('./HarnessesPane.svelte', import.meta.url), 'utf8')
// The pane and the section components that now hold its markup, for markup checks.
const paneMarkup = ['HarnessesPane', 'HarnessTabs', 'HarnessList', 'InstallControls', 'AccountsSection', 'ProvidersSection', 'SettingsImportSection', 'HarnessToolsSection', 'ToolsTab']
  .map((name) => readFileSync(new URL(`./${name}.svelte`, import.meta.url), 'utf8'))
  .join('\n')
// The helpers the pane's functions call from their own script.
const helpers = { errorText, failText: (e: unknown, fallback: string) => (e ? errorText(e) : fallback) }
// A question the pane asks before a sign-in or switch goes ahead.
interface Confirmation {
  kind: string
  action: string
  alternative: string
  lines: string[]
  run(): Promise<void>
  runAlternative(): Promise<void>
}
const recommendation = (status: string, version?: string): RecommendedVersion => ({ harness: 'codex', source: 'npm', status, version })
const account = (id: string, harness: string): HarnessAccount => ({ id, harness, label: '', home: '', identity: '', status: 'ready', active: false, repos: null })

test('harnessRowState derives ready, partial, not signed in, and switch pending states', () => {
  const snapshot = loose({
    capabilities: [
      { harness: 'claude-code', status: 'ready', reason: '', installSupported: true, mcp: true, skills: true, sharedPersonalLogin: false },
      { harness: 'codex', status: 'partial', reason: 'Reads personal skills from ~/.agents/skills.', installSupported: true, mcp: true, skills: true, sharedPersonalLogin: false },
      { harness: 'antigravity', status: 'partial', reason: 'Shares login with personal CLI.', installSupported: true, mcp: true, skills: true, sharedPersonalLogin: true },
      { harness: 'grok', status: 'partial', reason: 'Home compatibility skills remain.', installSupported: true, mcp: true, skills: true, sharedPersonalLogin: false },
    ],
    bundles: [
      { harness: 'claude-code', version: '2.1.299', executable: '/dyna/claude' },
      { harness: 'codex', version: '0.61.0', executable: '/dyna/codex' },
      { harness: 'antigravity', version: '1.3.2', executable: '/dyna/agy' },
    ],
    accounts: [
      { id: 'acc-1', harness: 'claude-code', label: 'work@acme.dev', status: 'ready', active: true },
      { id: 'acc-2', harness: 'codex', label: 'me@personal', status: 'ready', active: true },
      { id: 'acc-3', harness: 'antigravity', label: 'me@mail', status: 'ready', active: true },
    ],
    profiles: [
      { profile: 'codex-prof', harness: 'codex', status: 'switch pending', reason: 'switch pending: 2 turns running', switchPending: true, runningTurns: 2 },
    ],
  })

  const claude = harnessRowState('claude-code', snapshot)
  assert.equal(claude.status, 'ready')
  assert.equal(claude.needsSignIn, false)
  assert.equal(claude.version, '2.1.299')

  const codex = harnessRowState('codex', snapshot)
  assert.equal(codex.status, 'switch pending')
  assert.equal(codex.switchPending, true)
  assert.equal(codex.runningTurns, 2)
  assert.match(codex.reason, /switch pending: 2 turns running/)

  const agy = harnessRowState('antigravity', snapshot)
  assert.equal(agy.status, 'partial')
  assert.equal(agy.sharedPersonalLogin, true)

  const grok = harnessRowState('grok', snapshot)
  assert.equal(grok.status, 'not installed')
  assert.equal(grok.needsSignIn, false)
  assert.equal(grok.needsInstall, true)

  const installing = harnessRowState('grok', { ...snapshot, installing: ['grok'] })
  assert.equal(installing.status, 'installing')
  assert.match(installing.reason, /installing its copy/)
})

test('catalog rows require current runtime IDs', () => {
  assert.equal(harnessMeta('claude-code').name, 'Claude Code')
  assert.equal(harnessMeta('claude').id, 'claude')
  assert.equal(toolEnabledForHarness({ enabledHarnesses: ['claude-code'] }, 'claude-code'), true)
  assert.equal(toolEnabledForHarness({ enabledHarnesses: ['claude', 'grok'] }, 'claude-code'), false)
  assert.equal(toolEnabledForHarness({ enabledHarnesses: ['claude', 'grok'] }, 'codex'), false)
  assert.equal(preferenceStatusLabel('allowed'), 'will copy')
  assert.equal(preferenceStatusLabel('unknown'), 'not imported (unknown)')
  assert.equal(toolSummary('mcp', { command: 'npx', args: ['@playwright/mcp'] }), 'npx @playwright/mcp')
  assert.equal(HARNESS_ORDER.length, 8)
})

test('tool selection sends the runtime harness ID to the catalog', async () => {
  const calls: unknown[][] = []
  const context = {
    ...harnessState, ...helpers, busy: false, stopped: false, error: '', notice: '', catalog: null,
    api: { setHarnessConfigEnabled: async (...args: unknown[]) => { calls.push(args); return {} } },
  }
  runFunctions(paneSource, ['toggleTool'], context)
  await call(context, 'toggleTool', 'mcp', 'proof', 'claude-code', true)
  assert.deepEqual(calls, [['mcp', 'proof', 'claude-code', true]])
})

test('shared personal login harnesses require confirmation before sign-in, restart, or switch', async () => {
  const calls: unknown[][] = []
  const context: { confirm: Confirmation | null; login: unknown; [name: string]: unknown } = {
    ...helpers,
    busy: false,
    stopped: false,
    login: null,
    loginFlows: [],
    confirm: null,
    error: '',
    notice: '',
    versionInput: '1.3.2',
    labelInput: 'Work',
    showSignInForm: false,
    showSwitchChooser: false,
    preview: null,
    snapshot: null,
    exactVersion,
    recommendedVersion,
    setInterval: () => 1,
    clearInterval: () => {},
    api: {
      beginPrivateRuntimeLogin: async (...args: unknown[]) => { calls.push(['begin', ...args]); return { accountId: 'a1', signIn: { phase: 'waiting' } } },
      restartPrivateRuntimeLogin: async (...args: unknown[]) => { calls.push(['restart', ...args]); return { accountId: 'a1', signIn: { phase: 'waiting' } } },
      switchPrivateRuntimeAccount: async (...args: unknown[]) => { calls.push(['switch', ...args]); return { accounts: [] } },
    },
  }
  runFunctions(paneSource, ['signInVersion', 'requestBeginLogin', 'performBeginLogin', 'requestRestartLogin', 'performRestartLogin', 'requestSwitchAccount', 'performSwitchAccount'], context)

  const sharedRow = { id: 'antigravity', meta: { name: 'Antigravity' }, version: '1.3.2', sharedPersonalLogin: true, capability: {} }
  call(context, 'requestBeginLogin', sharedRow)
  assert.equal(calls.length, 0)
  assert.equal(need(context.confirm).kind, 'begin-login')
  await need(context.confirm).run()
  assert.deepEqual(calls[0], ['begin', 'antigravity', '1.3.2', 'Work', true])

  context.login = null
  call(context, 'requestRestartLogin', sharedRow, { id: 'acc-1', label: 'Personal' })
  assert.equal(calls.length, 1)
  assert.equal(need(context.confirm).kind, 'restart-login')
  await need(context.confirm).run()
  assert.deepEqual(calls[1], ['restart', 'acc-1', true])

  context.login = null
  call(context, 'requestSwitchAccount', sharedRow, { id: 'acc-2', label: 'Other', active: false })
  assert.equal(calls.length, 2)
  assert.equal(need(context.confirm).kind, 'switch-account')
  await need(context.confirm).run()
  assert.deepEqual(calls[2], ['switch', 'acc-2', true])
})

test('settings import requires matching preview hash and account', async () => {
  const calls: unknown[][] = []
  const context: { preview: unknown; [name: string]: unknown } = {
    ...helpers,
    busy: false,
    stopped: false,
    error: '',
    notice: '',
    snapshot: null,
    preview: { accountId: 'acc-1', harness: 'codex', hash: 'hash-123', keys: [{ key: 'theme', status: 'allowed' }] },
    api: {
      importPrivateConfig: async (...args: unknown[]) => { calls.push(args); return { accounts: [] } },
    },
  }
  runFunctions(paneSource, ['confirmImport'], context)

  await call(context, 'confirmImport', { id: 'other', harness: 'codex' })
  assert.equal(calls.length, 0)
  await call(context, 'confirmImport', { id: 'acc-1', harness: 'codex' })
  assert.deepEqual(calls, [['acc-1', 'hash-123']])
  assert.equal(context.preview, null)
})

test('saved native accounts report not signed in and missing copies cannot be ready', () => {
  const snapshot = (status: string, bundles: object[]) => loose({
    capabilities: [{ harness: 'codex', status: 'partial', reason: 'Personal skills remain.' }],
    bundles,
    accounts: [{ id: 'saved', harness: 'codex', status, active: true }],
  })
  const bundles = [{ harness: 'codex', version: '0.61.0' }]
  assert.equal(harnessRowState('codex', snapshot('not signed in', bundles)).needsSignIn, true)
  assert.equal(harnessRowState('codex', snapshot('ready', bundles)).status, 'partial')
  assert.equal(harnessRowState('codex', snapshot('ready', [])).status, 'not installed')
})

test('private copy versions accept pinned releases and reject ranges or shell text', () => {
  for (const version of ['2.1.299', '1.0.0-beta.2', '1.0.0+build.3', '2026.10.17-abc123']) assert.equal(exactVersion(version), true)
  for (const version of ['latest', '^1.0.0', '1', '1.0', '1.0.0;echo', '01.0.0']) assert.equal(exactVersion(version), false)
})

test('invalid MCP arguments stop before any catalog write', async () => {
  const calls: { enabledHarnesses: string[]; args: string[] }[] = []
  const context: { args: string; error: string; [name: string]: unknown } = {
    errorText,
    busy: false, error: '', name: 'browser', kind: 'mcp', mode: 'command',
    args: '["valid", 3]', command: 'npx', env: '',
    api: { addHarnessMCP: async (entry: { enabledHarnesses: string[]; args: string[] }) => { calls.push(entry); return {} } },
    onsave: () => {},
  }
  runFunctions(readFileSync(new URL('./ToolEditor.svelte', import.meta.url), 'utf8'), ['save'], context)
  await call(context, 'save')
  assert.equal(calls.length, 0)
  assert.match(context.error, /array of strings/)
  context.args = '["-y", "@playwright/mcp"]'
  await call(context, 'save')
  assert.equal(calls.length, 1)
  assert.deepEqual(Array.from(need(calls[0]).enabledHarnesses), [])
  assert.deepEqual(Array.from(need(calls[0]).args), ['-y', '@playwright/mcp'])
})


test('installing a private copy changes the row action from install to sign-in', async () => {
  const context: { snapshot: Partial<HarnessSnapshot> | null; [name: string]: unknown } = {
    ...helpers,
    busy: false, stopped: false, error: '', notice: '', installing: '', chooseVersion: false, snapshot: { bundles: [], accounts: [] }, exactVersion,
    api: { installPrivateRuntime: async (harness: string, version: string) => ({ bundles: [{ harness, version }], accounts: [] }) },
  }
  assert.equal(harnessRowState('codex', context.snapshot).needsInstall, true)
  assert.equal(harnessRowState('codex', context.snapshot).needsSignIn, false)
  runFunctions(paneSource, ['installVersion'], context)
  await call(context, 'installVersion', { id: 'codex', meta: { name: 'Codex' } }, '0.61.0')
  const installed = harnessRowState('codex', context.snapshot)
  assert.equal(installed.needsInstall, false)
  assert.equal(installed.status, 'not signed in')
  assert.equal(installed.needsSignIn, true)
})

test('selected private version takes precedence over the newest retained bundle', () => {
  const snapshot = (selected: string) => loose({ bundles: [{ harness: 'codex', version: '3.0.0' }, { harness: 'codex', version: '2.0.0' }], selectedVersions: { codex: selected } })
  assert.equal(harnessRowState('codex', snapshot('2.0.0')).version, '2.0.0')
  assert.equal(harnessRowState('codex', snapshot('1.0.0')).version, '3.0.0')
})

test('only verified recommendation statuses permit automatic installation', () => {
  for (const status of ['pinned', 'resolved']) assert.equal(recommendedVersion({ recommendation: recommendation(status, '2.0.0') }), '2.0.0')
  for (const status of ['unavailable', 'unsupported', 'unknown']) assert.equal(recommendedVersion({ recommendation: recommendation(status, '2.0.0') }), '')
  assert.equal(recommendedVersion({ recommendation: recommendation('resolved', 'latest') }), '')
})

// The pane's sign-in functions with an api the test controls.
interface SignInContext {
  login: { signIn: { phase: string } } | null
  confirm: Confirmation | null
  showSignInForm: boolean
  snapshot: { accounts: { id: string }[] } | null
  [name: string]: unknown
}
function signInContext(found: PersonalLogin | null) {
  const calls: unknown[][] = []
  const context: SignInContext = {
    ...helpers,
    busy: false, scanning: '', stopped: false, login: null, loginFlows: [], confirm: null, error: '', notice: '', labelInput: '', versionInput: '', showSignInForm: false,
    snapshot: null, pendingLogin: null, recommendedVersion, exactVersion, personalLoginChoice,
    setInterval: () => 1, clearInterval: () => {},
    api: {
      privateRuntimes: async () => ({}),
      findPersonalLogin: async (...args: unknown[]) => { calls.push(['find', ...args]); return found },
      usePersonalLogin: async (...args: unknown[]) => { calls.push(['use', ...args]); return { accounts: [{ id: 'p', harness: 'codex', active: true }] } },
      beginPrivateRuntimeLogin: async (...args: unknown[]) => { calls.push(['begin', ...args]); return { accountId: 'a1', signIn: { phase: 'waiting' } } },
      restartPrivateRuntimeLogin: async (...args: unknown[]) => { calls.push(['restart', ...args]); return { accountId: 'a1', signIn: { phase: 'waiting' } } },
    },
  }
  runFunctions(paneSource, ['signIn', 'signInVersion', 'findPersonalLogin', 'usePersonalLogin', 'requestBeginLogin', 'performBeginLogin', 'requestRestartLogin', 'performRestartLogin'], context)
  return { calls, context }
}

test('Sign in uses the recommended version when Dyna knows no installed one, then looks for a personal login', async () => {
  const { calls, context } = signInContext(null)
  const row = { id: 'codex', meta: { name: 'Codex' }, version: '', needsInstall: true, capability: {}, recommendation: { status: 'unavailable', version: '2.0.0' } }
  call(context, 'signIn', row)
  assert.equal(calls.length, 0)
  assert.equal(context.showSignInForm, true)
  row.recommendation = { status: 'resolved', version: '3.0.0' }
  await call(context, 'signIn', row)
  // Nothing found: the normal sign-in starts without a dialog.
  assert.deepEqual(calls, [['find', 'codex', '3.0.0'], ['begin', 'codex', '3.0.0', '', false]])
  assert.equal(need(context.login).signIn.phase, 'waiting')
})

test('Sign in offers the personal login, and either choice works', async () => {
  const row = { id: 'codex', meta: { name: 'Codex' }, version: '1.0.0', capability: {} }
  const personal: PersonalLogin = { harness: 'codex', label: 'owner@example.test', saved: false }
  let { calls, context } = signInContext(personal)
  await call(context, 'signIn', row)
  assert.equal(need(context.confirm).kind, 'personal-login')
  assert.equal(need(context.confirm).action, 'Use this account')
  assert.equal(need(context.confirm).alternative, 'Sign in instead')
  assert.match(String(need(context.confirm).lines[0]), /owner@example\.test/)
  await need(context.confirm).run()
  assert.deepEqual(calls.slice(1), [['use', 'codex']])
  assert.equal(context.confirm, null)
  assert.equal(need(need(context.snapshot).accounts[0]).id, 'p')

  ;({ calls, context } = signInContext(personal))
  await call(context, 'signIn', row)
  await need(context.confirm).runAlternative()
  assert.deepEqual(calls.slice(1), [['begin', 'codex', '1.0.0', '', false]])
})

test('a personal login already saved in Dyna offers only another account', async () => {
  const row = { id: 'codex', meta: { name: 'Codex' }, version: '1.0.0', capability: {} }
  const { calls, context } = signInContext({ harness: 'codex', label: 'owner@example.test', saved: true })
  await call(context, 'signIn', row)
  assert.match(String(need(context.confirm).lines[0]), /already saved in Dyna/)
  assert.equal(need(context.confirm).alternative, '')
  await need(context.confirm).run()
  assert.deepEqual(calls.slice(1), [['begin', 'codex', '1.0.0', '', false]])
})

test('Sign in on a saved account signs in again without a scan', async () => {
  const { calls, context } = signInContext(null)
  const saved = { id: 'acc-1', label: 'Work' }
  await call(context, 'signIn', { id: 'codex', meta: { name: 'Codex' }, version: '1.0.0', capability: {}, activeAccount: saved })
  assert.deepEqual(calls, [['restart', 'acc-1', false]])
  calls.length = 0
  context.login = null
  await call(context, 'signIn', { id: 'codex', meta: { name: 'Codex' }, version: '1.0.0', capability: {}, activeAccount: saved }, true)
  assert.equal(need(calls[0])[0], 'find')
})

test('private login recovery uses saved account metadata and keeps multiple cards', () => {
  const logins: PrivateLogin[] = [{ accountId: 'a', signIn: { phase: 'waiting' } }, { accountId: 'b', signIn: { phase: 'waiting' }, warning: 'Shared login' }]
  const flows = recoveredLogins(logins, [account('a', 'claude-code'), account('b', 'codex')])
  assert.deepEqual(flows.map((flow) => flow.harness), ['claude-code', 'codex'])
  assert.equal(need(flows[1]).warning, 'Shared login')
  assert.deepEqual(recoveredLogins([{ accountId: 'missing', signIn: { phase: 'waiting' } }], []), [])
  // A Pi provider sign-in has no account; the engine names its harness.
  assert.deepEqual(recoveredLogins([{ accountId: 'pi:provider', harness: 'pi', signIn: { phase: 'waiting' } }], []).map((flow) => flow.harness), ['pi'])
})

test('successful login cancellation accepts null, clears the flow, and reloads saved accounts', async () => {
  const calls: string[] = [], next = { accountId: 'b', harness: 'codex' }
  const context: { login: { accountId: string } | null; pendingLogin: { accountId: string } | null; loginFlows: unknown[]; busy: boolean; [name: string]: unknown } = {
    ...helpers,
    busy: false, stopped: false, login: { accountId: 'a' }, loginFlows: [{ accountId: 'a' }, next], pendingLogin: { accountId: 'a' },
    error: '', notice: '', api: { cancelPrivateRuntimeLogin: async (id: string) => { calls.push(id); return null } }, load: async () => {},
  }
  runFunctions(paneSource, ['cancelLogin'], context)
  await call(context, 'cancelLogin')
  assert.deepEqual(calls, ['a'])
  assert.equal(need(context.login).accountId, 'b')
  assert.equal(need(context.pendingLogin).accountId, 'b')
  assert.equal(context.loginFlows.length, 1)
  assert.equal(context.busy, false)
})

// The pane state that load reads and writes.
interface LoadContext {
  error: string
  loading: boolean
  snapshot: Partial<HarnessSnapshot> | null
  [name: string]: unknown
}

test('older backend snapshots load without requesting an unavailable login API', async () => {
  const context: LoadContext = {
    ...helpers,
    stopped: false, loading: false, error: '', snapshot: null, catalog: null,
    api: { privateRuntimes: async () => ({ bundles: [] }), harnessConfig: async () => ({ mcp: [], skills: [] }), privateRuntimeLogins: async () => { throw new Error('Unavailable API must not be called') } },
  }
  runFunctions(paneSource, ['load'], context)
  await call(context, 'load')
  assert.equal(context.error, '')
  assert.equal(context.loading, false)
  assert.equal(context.snapshot?.bundles?.length, 0)
})

test('Refresh checks current metadata while normal page loads use cached metadata', async () => {
  const calls: string[] = []
  const context: LoadContext = {
    ...helpers,
    stopped: false, loading: false, error: '', snapshot: null, catalog: null,
    api: {
      privateRuntimes: async () => { calls.push('cached'); return { bundles: [] } },
      refreshPrivateRuntimes: async () => { calls.push('fresh'); return { bundles: [{ harness: 'codex', version: '1.0.0' }] } },
      harnessConfig: async () => ({ mcp: [], skills: [] }),
    },
  }
  runFunctions(paneSource, ['load'], context)
  await call(context, 'load')
  await call(context, 'load', true)
  assert.deepEqual(calls, ['cached', 'fresh'])
  assert.equal(need(context.snapshot?.bundles?.[0]).version, '1.0.0')
  assert.match(paneSource, /onclick=\{\(\) => load\(true\)\}/)
})

test('a full page reload recovers the private login card from the backend', async () => {
  const context: { login: { signIn: { phase: string }; harness: string } | null; pendingLogin: { accountId: string } | null; error: string; [name: string]: unknown } = {
    ...helpers,
    stopped: false, loading: false, error: '', snapshot: null, catalog: null, loginFlows: [], login: null, pendingLogin: null, recoveredLogins,
    api: {
      privateRuntimes: async () => ({ selectedVersions: {}, accounts: [{ id: 'a', harness: 'codex' }] }),
      harnessConfig: async () => ({ mcp: [], skills: [] }),
      privateRuntimeLogins: async () => [{ accountId: 'a', signIn: { phase: 'waiting' } }],
    },
  }
  runFunctions(paneSource, ['load'], context)
  await call(context, 'load')
  assert.equal(context.error, '')
  assert.equal(need(context.login).signIn.phase, 'waiting')
  assert.equal(need(context.login).harness, 'codex')
  assert.equal(need(context.pendingLogin).accountId, 'a')
})

test('not-signed-in rows expose Sign in on installed harness rows', () => {
  for (const harness of ['muse', 'opencode']) {
    const snapshot = loose({ bundles: [{ harness, version: '2.0.0' }], accounts: [{ id: 'saved', harness, active: true, status: 'not signed in' }] })
    const row = harnessRowState(harness, snapshot)
    assert.equal(row.needsSignIn, true)
    assert.equal(row.needsInstall, false)
    assert.equal(row.status, 'not signed in')
    assert.equal(need(row.activeAccount).id, 'saved')
  }
})
test('shared-login capability overrides legacy harness defaults', () => {
  for (const harness of ['muse', 'antigravity']) {
    for (const sharedPersonalLogin of [false, true]) {
      const snapshot = loose({ capabilities: [{ harness, sharedPersonalLogin }] })
      assert.equal(harnessRowState(harness, snapshot).sharedPersonalLogin, sharedPersonalLogin)
    }
    assert.equal(harnessRowState(harness, {}).sharedPersonalLogin, true)
    assert.equal(harnessRowState(harness, loose({ capabilities: [{ harness }] })).sharedPersonalLogin, true)
  }
  assert.equal(harnessRowState('codex', {}).sharedPersonalLogin, false)
})

test('OpenCode native login rejection offers Sign in despite saved ready metadata', () => {
  const row = harnessRowState('opencode', loose({
    bundles: [{ harness: 'opencode', version: '2.0.0' }],
    accounts: [{ id: 'saved', harness: 'opencode', active: true, status: 'ready' }],
    profiles: [{ harness: 'opencode', status: 'not signed in' }],
  }))
  assert.equal(row.needsInstall, false)
  assert.equal(row.needsSignIn, true)
  assert.equal(row.status, 'not signed in')
})

test('a status check that timed out shows as unavailable, not ready', () => {
  const row = harnessRowState('claude-code', loose({
    bundles: [{ harness: 'claude-code', version: '2.0.0' }],
    accounts: [{ id: 'a', harness: 'claude-code', active: true, status: 'ready' }],
    profiles: [{ harness: 'claude-code', status: 'unavailable', reason: 'The status check did not finish in time. Refresh to try again.' }],
  }))
  assert.equal(row.status, 'unavailable')
  assert.equal(row.reason, 'The status check did not finish in time. Refresh to try again.')
})

test('a row offers an update only when the recommended version is newer than Dyna’s copy', () => {
  const snap = (recommended: string) => loose({ bundles: [{ harness: 'codex', version: '0.9.0' }], selectedVersions: { codex: '0.9.0' }, recommendedVersions: [{ harness: 'codex', version: recommended, status: 'resolved' }] })
  assert.equal(harnessRowState('codex', snap('0.10.0')).update, '0.10.0')
  assert.equal(harnessRowState('codex', snap('0.9.0')).update, '')
  assert.equal(harnessRowState('codex', snap('0.8.5')).update, '')
  assert.equal(harnessRowState('codex', loose({ recommendedVersions: [{ harness: 'codex', version: '1.0.0', status: 'resolved' }] })).update, '')
})

test('the Harnesses pane shows actions, not status chips or warning boxes', () => {
  assert.doesNotMatch(paneMarkup, /class="pill|pillLabel|class="note/)
  for (const action of ['Sign in', 'Add account', 'Remove', 'Uninstall', 'Update to']) assert.match(paneMarkup, new RegExp(action), action)
  assert.match(paneSource, /api\.uninstallPrivateRuntime\(/)
})

test('a row’s second line names the account and Dyna’s version', () => {
  const bundles = [{ harness: 'codex', version: '0.9.0' }]
  assert.equal(harnessStateLine('codex', loose({ bundles, accounts: [{ id: 'a', harness: 'codex', label: 'Work', active: true, status: 'ready' }] })), 'Work · 0.9.0')
  assert.equal(harnessStateLine('codex', loose({ bundles, accounts: [] })), 'Not signed in · 0.9.0')
  assert.equal(harnessStateLine('codex', {}), 'Not installed')
})
function harnessStateLine(id: string, snapshot: Partial<HarnessSnapshot>) { return rowLine(harnessRowState(id, snapshot)) }

test('a harness that is not installed offers Install, not Sign in, and installs the version Dyna knows or the user typed', async () => {
  const calls: unknown[][] = []
  const context: { notice: string; installing: string; error: string; chooseVersion: boolean; versionInput: string; [name: string]: unknown } = {
    ...harnessState, ...helpers, busy: false, stopped: false, error: '', notice: '', installing: '', versionInput: '', chooseVersion: false, snapshot: null, selectedId: 'grok',
    selectHarness: () => {},
    api: { installPrivateRuntime: async (...args: unknown[]) => { calls.push(args); return { bundles: [] } } },
  }
  runFunctions(paneSource, ['install', 'installVersion'], context)
  const row = { id: 'grok', meta: { name: 'Grok' }, needsInstall: true, capability: {}, recommendation: { status: 'resolved', version: '1.0.62' } }
  await call(context, 'install', row)
  assert.deepEqual(calls, [['grok', '1.0.62']])
  assert.equal(context.notice, '', 'the version under the title says it; no extra line')
  assert.equal(context.installing, '')
  row.recommendation = { status: 'unavailable', version: '' }
  await call(context, 'install', row)
  assert.equal(calls.length, 1)
  assert.match(context.error, /version number/)
  assert.equal(context.chooseVersion, true, 'the version field opens when Dyna knows no version')
  context.versionInput = ' 1.2.3 '
  await call(context, 'install', row)
  assert.deepEqual(calls[1], ['grok', '1.2.3'])

  const rowActions = paneMarkup.slice(paneMarkup.indexOf('{#if r.installing}'), paneMarkup.indexOf('{:else if r.update}'))
  assert.ok(rowActions.indexOf('{:else if r.needsInstall}') < rowActions.indexOf('Sign in'), 'Install comes before Sign in')
  assert.match(rowActions, /role="progressbar"/)
  const panel = paneMarkup.slice(paneMarkup.indexOf('<aside class="panel"'))
  assert.ok(panel.indexOf('{#if !r.needsInstall && !r.installing}') < panel.indexOf('<h3 class="section-label">Settings import</h3>'), 'import and tools wait for the install')
  assert.doesNotMatch(paneMarkup, /Sign in or start a session to install/)
})

test('a version typed for one harness never installs another', async () => {
  const calls: unknown[][] = []
  const context: { selectedId: string; chooseVersion: boolean; versionInput: string; [name: string]: unknown } = {
    ...harnessState, ...helpers, busy: false, stopped: false, error: '', notice: '', installing: '', versionInput: '', chooseVersion: false,
    snapshot: null, selectedId: 'claude-code', preview: null, removing: '', showSignInForm: false, detailPanel: null, rows: [],
    tick: async () => {}, motion: (ms: number) => ms, BASE: 200,
    api: { installPrivateRuntime: async (...args: unknown[]) => { calls.push(args); return { bundles: [] } } },
  }
  runFunctions(paneSource, ['selectHarness', 'install', 'installVersion'], context)
  const codex = { id: 'codex', meta: { name: 'Codex' }, needsInstall: true, capability: {}, recommendation: { status: 'resolved', version: '0.163.0' } }
  context.chooseVersion = true
  context.versionInput = '2.1.299'
  await call(context, 'install', codex)
  assert.deepEqual(calls, [['codex', '0.163.0']])
  assert.equal(context.selectedId, 'codex')
  context.chooseVersion = true
  context.versionInput = '0.160.0'
  await call(context, 'install', codex)
  assert.deepEqual(calls[1], ['codex', '0.160.0'])
})


test('updates follow semantic precedence and keep vendor version ordering', () => {
  const update = (installed: string, latest: string) => harnessRowState('codex', loose({
    bundles: [{ harness: 'codex', version: installed }],
    recommendedVersions: [{ harness: 'codex', version: latest, status: 'resolved' }],
  })).update
  assert.equal(update('1.0.0-beta.1', '1.0.0'), '1.0.0')
  assert.equal(update('1.0.0', '1.0.0-beta.1'), '')
  assert.equal(update('1.0.0+build.1', '1.0.0+build.2'), '')
  assert.equal(update('1.0.0-beta.2', '1.0.0-beta.10'), '1.0.0-beta.10')
  assert.equal(update('1.0.0-1', '1.0.0-alpha'), '1.0.0-alpha')
  assert.equal(update('1.0.0-beta', '1.0.0-beta.1'), '1.0.0-beta.1')
  assert.equal(update('1.4.10-R9999.1', '1.4.10-R10000.1'), '1.4.10-R10000.1')
  assert.equal(update('2026.02.01-2', '2026.02.01-10'), '2026.02.01-10')
})


test('Pi and OpenCode offer OAuth login and both keep their provider-key forms', () => {
  assert.equal(hasSubscriptionLogin('pi'), true)
  assert.equal(hasSubscriptionLogin('opencode'), true)
  assert.equal(hasSubscriptionLogin('codex'), true)
  assert.deepEqual(HARNESS_ORDER.filter((h) => takesProviderKeys(h.id)).map((h) => h.id), ['opencode', 'pi'])
  const snapshot = loose({
    capabilities: [{ harness: 'pi', status: 'ready', installSupported: true }, { harness: 'opencode', status: 'ready', installSupported: true }],
    bundles: [{ harness: 'pi', version: '1.3.0', executable: '/dyna/pi' }, { harness: 'opencode', version: '2.0.32', executable: '/dyna/opencode' }],
    accounts: [],
  })
  const pi = harnessRowState('pi', snapshot)
  assert.equal(pi.needsSignIn, true)
  assert.equal(pi.status, 'not signed in')
  assert.equal(rowLine(pi), 'Not signed in · 1.3.0')
  const opencode = harnessRowState('opencode', snapshot)
  assert.equal(opencode.needsSignIn, true)
  assert.equal(rowLine(opencode), 'Not signed in · 2.0.32')
})

test('filterProviders matches every typed word against name or ID, ignoring case', () => {
  const choices = [{ id: 'zai', name: 'ZAI Coding Plan' }, { id: 'zai-coding-cn', name: 'ZAI Coding Plan (China)' }, { id: 'openrouter', name: 'OpenRouter' }]
  assert.equal(filterProviders(choices, '').length, 3)
  assert.deepEqual(filterProviders(choices, 'ZAI').map((c) => c.id), ['zai', 'zai-coding-cn'])
  assert.deepEqual(filterProviders(choices, 'zai china').map((c) => c.id), ['zai-coding-cn'])
  assert.deepEqual(filterProviders(choices, 'open  rout').map((c) => c.id), ['openrouter'])
  assert.deepEqual(filterProviders(choices, 'nothing'), [])
  assert.deepEqual(filterProviders(undefined, 'x'), [])
})

test('the key reaches only the add call and is cleared after saving', () => {
  const dialog = readFileSync(new URL('./ProviderKeyDialog.svelte', import.meta.url), 'utf8')
  const calls = dialog.match(/api\.harnessProviderKeys\([^)]*\)/g) ?? []
  assert.deepEqual(calls, ["api.harnessProviderKeys(harness, 'choices')", "api.harnessProviderKeys(harness, 'add', picked.id, key)"])
  assert.match(dialog, /type="password"/)
  assert.ok(dialog.indexOf("key = ''") > dialog.indexOf("'add', picked.id, key"))
  assert.doesNotMatch(dialog, /console\.|localStorage|sessionStorage/)
  // A pending sign-in keeps its card reachable. Pi has no account list: its card sits with the providers.
  assert.match(paneSource, /\{#if r\.id !== 'pi' && \(hasSubscriptionLogin\(r\.id\) \|\| \(login && login\.harness === r\.id\)\)\}/)
  assert.match(paneSource, /login=\{login && login\.harness === r\.id \? login : null\}\n\s+\{signInState\}\n\s+onsignin=\{\(\) => signIn\(r, true\)\}/)
  assert.match(paneSource, /onsaved=\{[^}]*load\(\)/)
  assert.match(paneSource, /api\.harnessProviderKeys\(id, 'remove', provider\)/)
})

test('current harness rows ignore unsupported cached aliases', () => {
  const row = harnessRowState('claude-code', loose({
    capabilities: [{ harness: 'claude', status: 'ready' }],
    bundles: [{ harness: 'claude', version: '1.0.0' }],
    accounts: [{ id: 'old', harness: 'claude', active: true }],
    profiles: [{ harness: 'claude', status: 'ready' }],
  }))
  assert.equal(row.needsInstall, true)
  assert.equal(row.capability, null)
  assert.deepEqual(row.accounts, [])
  assert.deepEqual(row.bundles, [])
})

// The engine says why Finish failed, for example a Google account it could
// not confirm. The pane keeps its words.
test('finish sign-in shows the engine message', async () => {
  const message = 'Google did not confirm the account of this Antigravity sign-in, so nothing was saved.'
  const context: { [name: string]: unknown } = {
    ...helpers, busy: false, stopped: false, error: '', notice: '', labelInput: '', pendingLogin: null,
    login: { accountId: 'a1', signIn: { phase: 'waiting' }, harness: 'antigravity' }, loginFlows: [],
    api: { finishPrivateRuntimeLogin: async () => { throw new Error(message) } },
  }
  runFunctions(paneSource, ['finishLogin'], context)
  await call(context, 'finishLogin')
  assert.equal(context.error, message)
  context.api = { finishPrivateRuntimeLogin: async () => { throw null } }
  await call(context, 'finishLogin')
  assert.equal(context.error, 'Sign-in is not complete yet.')
})

test('Antigravity sign-in does not promise a personal login copy', () => {
  assert.match(paneMarkup, /\{#if row\.id !== 'antigravity'\}\s*<p class="muted small">If you already use \{row\.meta\.name\}, Dyna can copy that login once\.<\/p>/)
})

// The shared card words each step of an in-app sign-in, and repeats the
// engine's reason when the sign-in failed.
test('sign-in card words each step', () => {
  assert.equal(harnessState.signInStatus({ phase: 'starting' }), 'Starting sign-in…')
  assert.equal(harnessState.signInStatus({ phase: 'waiting' }), 'Waiting for you to approve the sign-in.')
  assert.equal(harnessState.signInStatus({ phase: 'waiting', message: 'Paste the final redirect URL.' }), 'Paste the final redirect URL.')
  assert.equal(harnessState.signInStatus({ phase: 'verifying' }), 'Checking the sign-in…')
  assert.equal(harnessState.signInStatus({ phase: 'succeeded' }), 'Signed in.')
  assert.equal(harnessState.signInStatus({ phase: 'cancelled' }), 'Sign-in cancelled.')
  assert.equal(harnessState.signInStatus({ phase: 'failed', message: 'Sign-in timed out. Try again.' }), 'Sign-in timed out. Try again.')
  assert.equal(harnessState.signInStatus({ phase: 'failed' }), 'Sign-in did not finish. Try again.')
  assert.equal(harnessState.signInStatus(null), 'Starting sign-in…')
})
