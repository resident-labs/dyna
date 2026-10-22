import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { healthChip, observedRows, driftText, probeRows, runProbe, since, unreadableOutput, type Observed, type ProfileView, type SupportItem, type HealthStatus, type SupportView } from './support.ts'

const item = (key: string, label: string, state: string, chip = ''): SupportItem => ({ key, label, state, reason: state === 'yes' ? '' : 'why', chip })
const items = (usage = 'yes'): SupportItem[] => [item('transcript', 'Transcript', 'yes'), item('toolEvents', 'Tool calls', 'yes'), item('usage', 'Token counts', usage, 'No token counts')]
const profile = (support: SupportView, more: { disabled?: boolean } = {}): ProfileView => ({ name: 'opus', harness: 'claude-code', support: { installed: true, binary: '/usr/bin/claude', version: '2.1.5', partial: false, status: 'healthy', items: items(), ...support }, ...more })
const ZERO = '0001-01-01T00:00:00Z'

const apiModule = '../../api.js?health-flow'
type ApiModule = { api: { configCheckProfile: (name: string, live: boolean) => Promise<unknown> } }
const hasConfigCheckProfile = (m: unknown): m is ApiModule => typeof m === 'object' && m !== null && 'api' in m && typeof m.api === 'object' && m.api !== null && 'configCheckProfile' in m.api && typeof m.api.configCheckProfile === 'function'
// The bridge result is checked field by field, so the probe flow never claims a shape it did not verify.
type ProbeCheck = { name: string; support: SupportView }
type Rec = Record<string, unknown>
const isRec = (v: unknown): v is Rec => typeof v === 'object' && v !== null && !Array.isArray(v)
// hasTypes checks each listed key, when present, against one typeof name.
const hasTypes = (v: Rec, type: string, keys: string[]): boolean => keys.every((k) => !(k in v) || typeof v[k] === type)
const healthStatuses: readonly HealthStatus[] = ['unknown', 'checking', 'healthy', 'unhealthy', 'output_not_understood', 'unverified_since_update', 'unverified_since_edit']
const isHealthStatus = (v: unknown): v is HealthStatus => healthStatuses.some((s) => s === v)
const isItem = (v: unknown): v is SupportItem => isRec(v) && ['key', 'label', 'state', 'reason', 'chip'].every((k) => typeof v[k] === 'string')
const isObserved = (v: unknown): v is Partial<Observed> => isRec(v) && hasTypes(v, 'string', ['at', 'lastTurnAt']) && hasTypes(v, 'boolean', ['running', 'successful', 'stdoutSeen', 'textSeen', 'toolSeen', 'positiveUsageSeen', 'recognizedOutput', 'probe', 'probePassed']) && hasTypes(v, 'number', ['recognizedRecords'])
const isSupportView = (v: unknown): v is SupportView => isRec(v) && hasTypes(v, 'string', ['binary', 'version', 'versionError', 'lastHealthyVersion', 'dialect', 'reason']) && hasTypes(v, 'boolean', ['installed', 'installable', 'partial']) && (!('status' in v) || isHealthStatus(v.status)) && (!('items' in v) || (Array.isArray(v.items) && v.items.every(isItem))) && (!('observed' in v) || isObserved(v.observed))
const isProbeCheck = (v: unknown): v is ProbeCheck => isRec(v) && typeof v.name === 'string' && isSupportView(v.support)

test('a healthy, fully supported profile shows no chip', () => {
  assert.equal(healthChip(profile({})), null)
  assert.equal(healthChip(profile({ status: 'unknown' })), null)
  assert.equal(healthChip(profile({ status: 'checking' })), null)
  assert.equal(healthChip({}), null)
})

test('each health state reads as plain words', () => {
  assert.equal(healthChip(profile({ partial: true, items: items('unavailable') }))?.text, 'Partly supported: no token counts')
  assert.equal(healthChip(profile({ status: 'unverified_since_update' }))?.text, 'Not verified since claude 2.1.5')
  assert.equal(healthChip(profile({ status: 'output_not_understood' }))?.text, 'Output not understood')
  assert.equal(healthChip(profile({ installed: false, status: 'unknown', versionError: 'not found' }))?.text, 'Harness not installed')
  assert.equal(healthChip(profile({ status: 'unverified_since_edit' }))?.text, 'Not verified since edit')
  assert.equal(healthChip(profile({ status: 'unhealthy' }))?.text, 'Last check failed')
})

test('one chip shows: install and health problems outrank a partial gap', () => {
  const gap = { partial: true, items: items('no') }
  assert.equal(healthChip(profile({ ...gap, status: 'output_not_understood' }))?.text, 'Output not understood')
  assert.equal(healthChip(profile({ ...gap, installed: false }))?.tone, 'bad')
  assert.equal(healthChip(profile({ ...gap }))?.tone, 'warn')
  assert.equal(healthChip(profile({ status: 'output_not_understood' }, { disabled: true })), null)
})

test('observed rows say what real turns showed', () => {
  const now = Date.parse('2026-10-17T12:00:00Z')
  const turn = { lastTurnAt: '2026-10-17T11:30:00Z', at: '2026-10-17T11:30:00Z', toolSeen: true, positiveUsageSeen: false }
  assert.deepEqual(observedRows(profile({ observed: turn }), now), [['Last real turn', '30 min ago'], ['Latest check', 'Real turn, 30 min ago'], ['Tool calls seen', 'Yes'], ['Token counts seen', 'No']])
  assert.deepEqual(observedRows(profile({}), now), [['Last real turn', 'None yet'], ['Latest check', 'None yet'], ['Tool calls seen', 'Not yet'], ['Token counts seen', 'Not yet']])
  const probe = observedRows(profile({ observed: { lastTurnAt: ZERO, at: '2026-10-17T11:59:50Z', probe: true, toolSeen: true } }), now)
  assert.deepEqual(probe.slice(0, 2), [['Last real turn', 'None yet'], ['Latest check', 'Test, just now']])
  assert.equal(since(ZERO), '')
})

test('drift text names the version change', () => {
  assert.equal(driftText(profile({ status: 'unverified_since_update', lastHealthyVersion: '2.0.0' })), 'Updated from 2.0.0. Not verified since.')
  assert.equal(driftText(profile({ lastHealthyVersion: '2.1.5' })), 'Same version as the last good check')
  assert.equal(driftText(profile({ version: '', versionError: 'no binary' })), 'Version unknown: no binary')
})

const probed = (o: Partial<Observed>, usage = 'yes', tools = 'yes'): ProfileView => ({ name: 'opus', support: { status: 'healthy', items: [item('toolEvents', 'Tool calls', tools), item('usage', 'Token counts', usage)], observed: { probe: true, textSeen: true, toolSeen: true, positiveUsageSeen: true, ...o } } })

test('probe result has one plain row per capability', () => {
  assert.deepEqual(probeRows(probed({})).map((r) => [r.label, r.state]), [['Reply text', 'yes'], ['Tool calls', 'yes'], ['Token counts', 'yes']])
  const rows = probeRows(probed({ toolSeen: false, positiveUsageSeen: false }))
  assert.deepEqual(rows.map((r) => r.state), ['yes', 'no', 'no'])
  assert.match(rows[1]?.words ?? '', /saw no tool call/)
  assert.deepEqual(probeRows(probed({}, 'unavailable', 'no')).map((r) => r.state), ['yes', 'na', 'na'])
  assert.equal(probeRows(probed({ textSeen: false }))[0]?.words, 'Dyna could not read a reply')
  assert.deepEqual(probeRows({ support: { items: [], observed: { probe: false } } }), [])
})

test('the probe flow calls the bridge with live true and updates the chip', async () => {
  const calls: unknown[][] = []
  Object.assign(globalThis, { document: { querySelector: () => null } })
  Object.assign(globalThis, {
    fetch: async (path: string, options?: RequestInit) => {
      assert.equal(path, '/api/ConfigCheckProfile')
      const body = options?.body
      assert.ok(typeof body === 'string')
      const parsed: unknown = JSON.parse(body)
      assert.ok(Array.isArray(parsed))
      const name: unknown = parsed[0]
      calls.push(parsed)
      return Response.json({ name, support: profile({ status: 'healthy' }).support })
    },
  })
  // The query string gives this test its own copy of the api module. TypeScript cannot resolve it, so the guard checks the shape.
  const mod: unknown = await import(apiModule)
  assert.ok(hasConfigCheckProfile(mod))
  assert.equal(isProbeCheck({ name: 'opus', support: { items: [{ key: 'usage' }] } }), false)
  assert.equal(isProbeCheck({ name: 'opus', support: { status: 'bogus' } }), false)
  assert.equal(isProbeCheck({ name: 'opus', support: { status: 'unverified_since_update' } }), true)
  const { api } = mod
  const r = await runProbe(async () => {
    const v = await api.configCheckProfile('opus', true)
    assert.ok(isProbeCheck(v), 'the probe result has the expected shape')
    return v
  })
  assert.deepEqual(calls, [['opus', true]])
  assert.equal(r.phase, 'done')
  assert.equal(healthChip({ ...profile({ status: 'unverified_since_update' }) })?.tone, 'warn')
  assert.ok('check' in r)
  assert.equal(healthChip({ name: 'opus', support: r.check.support }), null)
})

test('the probe flow reports errors and never throws', async () => {
  const r = await runProbe(async () => { throw new Error('read-only mode') })
  assert.deepEqual(r, { phase: 'failed', error: 'read-only mode' })
  // A message of any truthy type shows as String(message), as the old e?.message || e did.
  const fn = () => 'x'
  assert.deepEqual(await runProbe(() => Promise.reject({ message: 42 })), { phase: 'failed', error: '42' })
  assert.deepEqual(await runProbe(() => Promise.reject({ message: fn })), { phase: 'failed', error: String(fn) })
})

test('the probe flow is time-boxed and does not wait for a stuck call', async () => {
  let fire = false
  let cleared = false
  const setTimer = (fn: () => void): number => { queueMicrotask(fn); fire = true; return 1 }
  const r = await runProbe(() => new Promise(() => {}), { setTimer, clearTimer: () => { cleared = true } })
  assert.deepEqual(r, { phase: 'timeout' })
  assert.ok(fire && cleared)
})

test('the editor shows progress while the probe runs and keeps the support button', () => {
  const src = readFileSync(new URL('./ProfileEditor.svelte', import.meta.url), 'utf8')
  assert.match(src, /Test this profile/)
  assert.match(src, /Ask an agent to add support/)
  assert.match(src, /test\?\.phase === 'running'/)
  assert.match(src, /onchecked\?\.\(r\.check\)/)
})

test('the session notice shows only for a live turn with unreadable output', () => {
  const live = { state: 'live', profile: 'opus' }
  const bad = (running: boolean): ProfileView => ({ support: { status: 'output_not_understood', observed: { running } } })
  assert.equal(unreadableOutput(live, bad(true)), true)
  assert.equal(unreadableOutput(live, bad(false)), false)
  assert.equal(unreadableOutput({ state: 'done' }, bad(true)), false)
  assert.equal(unreadableOutput(live, { support: { status: 'healthy', observed: { running: true } } }), false)
  assert.equal(unreadableOutput(live, null), false)
  assert.equal(unreadableOutput(null, bad(true)), false)
})

test('the session view renders the notice with a profile link', () => {
  const src = readFileSync(new URL('../sessions/Session.svelte', import.meta.url), 'utf8')
  assert.match(src, /\{#if unreadable\}[\s\S]*class="notice"[\s\S]*onharness\(m\.profile\)/)
  assert.match(src, /api\.configCheckProfile\(name, false\)/)
})

test('a harness Dyna can install shows no chip; one it cannot names the fix', () => {
  assert.equal(healthChip(profile({ installed: false, installable: true, versionError: 'Dyna installs this harness on first use.' })), null)
  const chip = healthChip(profile({ installed: false, versionError: 'Install pi on the Harnesses page first.' }))
  assert.equal(chip?.tone, 'bad')
  assert.equal(chip?.title, 'Install pi on the Harnesses page first.')
})
