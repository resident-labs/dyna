import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import * as harnessState from './state.ts'
import {
  HARNESS_ORDER,
  exactVersion,
  harnessMeta,
  harnessRowState,
  preferenceStatusLabel,
  recommendedVersion,
  rowLine,
  toolEnabledForHarness,
  toolSummary,
} from './state.ts'
import { errorText } from '../../errorText.ts'
import { call, runFunctions } from '../../../../test/svelte-source.ts'
import { need } from '../../../../test/need.ts'
import type { HarnessSnapshot, RecommendedVersion } from '../../api/harness.ts'

// Snapshot data that leaves out fields the engine may omit, so the helpers read it as the engine sends it.
const loose = (value: object): Partial<HarnessSnapshot> => JSON.parse(JSON.stringify(value))
const paneSource = readFileSync(new URL('./HarnessesPane.svelte', import.meta.url), 'utf8')
// The pane and the section components that now hold its markup, for markup checks.
const paneMarkup = ['HarnessesPane', 'HarnessTabs', 'HarnessList', 'InstallControls', 'SettingsImportSection', 'HarnessToolsSection', 'ToolsTab']
  .map((name) => readFileSync(new URL(`./${name}.svelte`, import.meta.url), 'utf8'))
  .join('\n')
// The helpers the pane's functions call from their own script.
const helpers = { errorText, failText: (e: unknown, fallback: string) => (e ? errorText(e) : fallback) }
const recommendation = (status: string, version?: string): RecommendedVersion => ({ harness: 'pi', source: 'npm', status, version })

test('harnessRowState derives ready, partial, not signed in, and switch pending states', () => {
  const snapshot = loose({
    capabilities: [
      { harness: 'claude-code', status: 'ready', reason: '', installSupported: true, mcp: true, skills: true, sharedPersonalLogin: false },
      { harness: 'muse', status: 'partial', reason: 'Reads personal skills from ~/.agents/skills.', installSupported: true, mcp: true, skills: true, sharedPersonalLogin: false },
      { harness: 'grok', status: 'partial', reason: 'Home compatibility skills remain.', installSupported: true, mcp: true, skills: true, sharedPersonalLogin: false },
    ],
    bundles: [
      { harness: 'claude-code', version: '2.1.299', executable: '/dyna/claude' },
      { harness: 'muse', version: '0.61.0', executable: '/dyna/muse' },
    ],
    accounts: [
      { id: 'acc-1', harness: 'claude-code', label: 'work@acme.dev', status: 'ready', active: true },
      { id: 'acc-2', harness: 'muse', label: 'me@personal', status: 'ready', active: true },
    ],
    profiles: [
      { profile: 'muse-prof', harness: 'muse', status: 'switch pending', reason: 'switch pending: 2 turns running', switchPending: true, runningTurns: 2 },
    ],
  })

  const claude = harnessRowState('claude-code', snapshot)
  assert.equal(claude.status, 'ready')
  assert.equal(claude.needsSignIn, false)
  assert.equal(claude.version, '2.1.299')

  const muse = harnessRowState('muse', snapshot)
  assert.equal(muse.status, 'switch pending')
  assert.equal(muse.switchPending, true)
  assert.equal(muse.runningTurns, 2)
  assert.match(muse.reason, /switch pending: 2 turns running/)

  const pi = harnessRowState('pi', snapshot)
  assert.equal(pi.status, 'not installed')
  assert.equal(pi.needsSignIn, false)
  assert.equal(pi.needsInstall, true)

  const installing = harnessRowState('pi', { ...snapshot, installing: ['pi'] })
  assert.equal(installing.status, 'installing')
  assert.match(installing.reason, /installing its copy/)
})

test('catalog rows require current runtime IDs', () => {
  assert.equal(harnessMeta('claude-code').name, 'Claude Code')
  assert.equal(harnessMeta('claude').id, 'claude')
  assert.equal(toolEnabledForHarness({ enabledHarnesses: ['claude-code'] }, 'claude-code'), true)
  assert.equal(toolEnabledForHarness({ enabledHarnesses: ['claude', 'grok'] }, 'claude-code'), false)
  assert.equal(toolEnabledForHarness({ enabledHarnesses: ['claude', 'grok'] }, 'muse'), false)
  assert.equal(preferenceStatusLabel('allowed'), 'will copy')
  assert.equal(preferenceStatusLabel('unknown'), 'not imported (unknown)')
  assert.equal(toolSummary('mcp', { command: 'npx', args: ['@playwright/mcp'] }), 'npx @playwright/mcp')
  assert.equal(HARNESS_ORDER.length, 4)
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

test('settings import requires matching preview hash and account', async () => {
  const calls: unknown[][] = []
  const context: { preview: unknown; [name: string]: unknown } = {
    ...helpers,
    busy: false,
    stopped: false,
    error: '',
    notice: '',
    snapshot: null,
    preview: { accountId: 'acc-1', harness: 'pi', hash: 'hash-123', keys: [{ key: 'theme', status: 'allowed' }] },
    api: {
      importPrivateConfig: async (...args: unknown[]) => { calls.push(args); return { accounts: [] } },
    },
  }
  runFunctions(paneSource, ['confirmImport'], context)

  await call(context, 'confirmImport', { id: 'other', harness: 'pi' })
  assert.equal(calls.length, 0)
  await call(context, 'confirmImport', { id: 'acc-1', harness: 'pi' })
  assert.deepEqual(calls, [['acc-1', 'hash-123']])
  assert.equal(context.preview, null)
})

test('saved native accounts report not signed in and missing copies cannot be ready', () => {
  const snapshot = (status: string, bundles: object[]) => loose({
    capabilities: [{ harness: 'pi', status: 'partial', reason: 'Personal skills remain.' }],
    bundles,
    accounts: [{ id: 'saved', harness: 'pi', status, active: true }],
  })
  const bundles = [{ harness: 'pi', version: '0.61.0' }]
  assert.equal(harnessRowState('pi', snapshot('not signed in', bundles)).needsSignIn, true)
  assert.equal(harnessRowState('pi', snapshot('ready', bundles)).status, 'partial')
  assert.equal(harnessRowState('pi', snapshot('ready', [])).status, 'not installed')
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

test('installing a private copy clears the install action', async () => {
  const context: { snapshot: Partial<HarnessSnapshot> | null; [name: string]: unknown } = {
    ...helpers,
    busy: false, stopped: false, error: '', notice: '', installing: '', chooseVersion: false, snapshot: { bundles: [], accounts: [] }, exactVersion,
    api: { installPrivateRuntime: async (harness: string, version: string) => ({ bundles: [{ harness, version }], accounts: [] }) },
  }
  assert.equal(harnessRowState('pi', context.snapshot).needsInstall, true)
  assert.equal(harnessRowState('pi', context.snapshot).needsSignIn, false)
  runFunctions(paneSource, ['installVersion'], context)
  await call(context, 'installVersion', { id: 'pi', meta: { name: 'Pi' } }, '0.61.0')
  const installed = harnessRowState('pi', context.snapshot)
  assert.equal(installed.needsInstall, false)
  assert.equal(installed.status, 'not signed in')
  assert.equal(installed.needsSignIn, true)
})

test('selected private version takes precedence over the newest retained bundle', () => {
  const snapshot = (selected: string) => loose({ bundles: [{ harness: 'pi', version: '3.0.0' }, { harness: 'pi', version: '2.0.0' }], selectedVersions: { pi: selected } })
  assert.equal(harnessRowState('pi', snapshot('2.0.0')).version, '2.0.0')
  assert.equal(harnessRowState('pi', snapshot('1.0.0')).version, '3.0.0')
})

test('only verified recommendation statuses permit automatic installation', () => {
  for (const status of ['pinned', 'resolved']) assert.equal(recommendedVersion({ recommendation: recommendation(status, '2.0.0') }), '2.0.0')
  for (const status of ['unavailable', 'unsupported', 'unknown']) assert.equal(recommendedVersion({ recommendation: recommendation(status, '2.0.0') }), '')
  assert.equal(recommendedVersion({ recommendation: recommendation('resolved', 'latest') }), '')
})

test('not-signed-in rows keep their harness state without a sign-in action', () => {
  for (const harness of ['muse', 'opencode']) {
    const snapshot = loose({ bundles: [{ harness, version: '2.0.0' }], accounts: [{ id: 'saved', harness, active: true, status: 'not signed in' }] })
    const row = harnessRowState(harness, snapshot)
    assert.equal(row.needsSignIn, true)
    assert.equal(row.needsInstall, false)
    assert.equal(row.status, 'not signed in')
    assert.equal(need(row.activeAccount).id, 'saved')
  }
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
  const snap = (recommended: string) => loose({ bundles: [{ harness: 'pi', version: '0.9.0' }], selectedVersions: { pi: '0.9.0' }, recommendedVersions: [{ harness: 'pi', version: recommended, status: 'resolved' }] })
  assert.equal(harnessRowState('pi', snap('0.10.0')).update, '0.10.0')
  assert.equal(harnessRowState('pi', snap('0.9.0')).update, '')
  assert.equal(harnessRowState('pi', snap('0.8.5')).update, '')
  assert.equal(harnessRowState('pi', loose({ recommendedVersions: [{ harness: 'pi', version: '1.0.0', status: 'resolved' }] })).update, '')
})

test('the Harnesses pane shows install actions only, with no account or sign-in section', () => {
  assert.doesNotMatch(paneMarkup, /class="pill|pillLabel|class="note/)
  for (const action of ['Uninstall', 'Update to']) assert.match(paneMarkup, new RegExp(action), action)
  assert.doesNotMatch(paneMarkup, /Sign in|Add account|AccountsSection|ProvidersSection|ProviderKeyDialog|SignInCard/)
  assert.doesNotMatch(paneSource, /api\.switchPrivateRuntimeAccount|api\.beginPrivateRuntimeLogin|api\.harnessProviderKeys/)
  assert.match(paneSource, /api\.uninstallPrivateRuntime\(/)
})

test('a row’s second line names the account and Dyna’s version', () => {
  const bundles = [{ harness: 'pi', version: '0.9.0' }]
  assert.equal(harnessStateLine('pi', loose({ bundles, accounts: [{ id: 'a', harness: 'pi', label: 'Work', active: true, status: 'ready' }] })), 'Work · 0.9.0')
  assert.equal(harnessStateLine('pi', loose({ bundles, accounts: [] })), 'Not signed in · 0.9.0')
  assert.equal(harnessStateLine('pi', {}), 'Not installed')
})
function harnessStateLine(id: string, snapshot: Partial<HarnessSnapshot>) { return rowLine(harnessRowState(id, snapshot)) }

test('a harness that is not installed offers Install and installs the version Dyna knows or the user typed', async () => {
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
  assert.ok(rowActions.indexOf('{:else if r.needsInstall}') >= 0, 'Install stays the installed row action')
  assert.match(rowActions, /role="progressbar"/)
  const panel = paneMarkup.slice(paneMarkup.indexOf('<aside class="panel"'))
  assert.ok(panel.indexOf('{#if !r.needsInstall && !r.installing}') < panel.indexOf('<h3 class="section-label">Settings import</h3>'), 'import and tools wait for the install')
  assert.doesNotMatch(paneMarkup, /Sign in or start a session to install/)
})

test('a version typed for one harness never installs another', async () => {
  const calls: unknown[][] = []
  const context: { selectedId: string; chooseVersion: boolean; versionInput: string; [name: string]: unknown } = {
    ...harnessState, ...helpers, busy: false, stopped: false, error: '', notice: '', installing: '', versionInput: '', chooseVersion: false,
    snapshot: null, selectedId: 'claude-code', preview: null, removing: '', detailPanel: null, rows: [],
    tick: async () => {}, motion: (ms: number) => ms, BASE: 200,
    api: { installPrivateRuntime: async (...args: unknown[]) => { calls.push(args); return { bundles: [] } } },
  }
  runFunctions(paneSource, ['selectHarness', 'install', 'installVersion'], context)
  const pi = { id: 'pi', meta: { name: 'Pi' }, needsInstall: true, capability: {}, recommendation: { status: 'resolved', version: '0.163.0' } }
  context.chooseVersion = true
  context.versionInput = '2.1.299'
  await call(context, 'install', pi)
  assert.deepEqual(calls, [['pi', '0.163.0']])
  assert.equal(context.selectedId, 'pi')
  context.chooseVersion = true
  context.versionInput = '0.160.0'
  await call(context, 'install', pi)
  assert.deepEqual(calls[1], ['pi', '0.160.0'])
})

test('updates follow semantic precedence and keep vendor version ordering', () => {
  const update = (installed: string, latest: string) => harnessRowState('pi', loose({
    bundles: [{ harness: 'pi', version: installed }],
    recommendedVersions: [{ harness: 'pi', version: latest, status: 'resolved' }],
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
