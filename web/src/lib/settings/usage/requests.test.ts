import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { requestGate } from './state.ts'
import { call, runFunctions } from '../../../../test/svelte-source.ts'

const component = (file: string): string => readFileSync(new URL(file, import.meta.url), 'utf8')

test('profile handoffs are one-shot and normal navigation cannot replay them', () => {
  const context: Record<string, unknown> = { pane: 'profiles', profileSelection: { harness: 'opencode' }, profileToOpen: 'old', usageAccountId: '' }
  runFunctions(component('../Settings.svelte'), ['selectPane', 'viewUsage', 'viewProfile'], context)
  call(context, 'viewUsage', 'account-a')
  assert.equal(context.profileSelection, null); assert.equal(context.profileToOpen, '')
  call(context, 'viewProfile', 'reviewer')
  assert.equal(context.pane, 'profiles'); assert.equal(context.profileToOpen, 'reviewer')
  call(context, 'selectPane', 'usage'); call(context, 'selectPane', 'profiles')
  assert.equal(context.profileToOpen, ''); assert.equal(context.profileSelection, null)
})

test('profile creation requires the active account and binds the private account', () => {
  const selections: unknown[] = []
  const account = { id: 'private-a', harness: 'opencode', routeId: 'opencode-go/fixture', active: false }
  const context: Record<string, unknown> = { account, oncreateprofile: (selection: unknown) => selections.push(JSON.parse(JSON.stringify(selection))) }
  runFunctions(component('./AccountDetails.svelte'), ['createProfile'], context)
  call(context, 'createProfile'); assert.equal(selections.length, 0)
  account.active = true; call(context, 'createProfile')
  assert.deepEqual(selections, [{ harness: 'opencode', routeId: 'opencode-go/fixture', connectionId: 'private-a' }])
})

test('Sign in on Usage closes the drawer and opens Harnesses', () => {
  let opened = 0
  const context: Record<string, unknown> = { modal: { account: { id: 'private-a' } }, onharnesses: () => { opened++ } }
  runFunctions(component('./UsagePane.svelte'), ['signIn'], context)
  call(context, 'signIn')
  assert.equal(context.modal, null)
  assert.equal(opened, 1)
})

test('Usage reads saved observations until an explicit provider refresh', async () => {
  let probes = 0
  const context: Record<string, unknown> = { mounted: true, gate: requestGate(), loading: false, error: '', snapshot: null, openedInitial: true, accountId: '', api: {
    accountUsageCached: async () => ({ accounts: [{ id: 'saved', windows: [{ usedPercent: 40 }] }] }),
    accountUsage: async (refresh: boolean) => { assert.equal(refresh, true); probes++; return { accounts: [{ id: 'saved', windows: [{ usedPercent: 45 }] }] } },
  } }
  runFunctions(component('./UsagePane.svelte'), ['load'], context)
  await call(context, 'load', false)
  assert.deepEqual(context.snapshot, { accounts: [{ id: 'saved', windows: [{ usedPercent: 40 }] }] })
  assert.equal(probes, 0)
  assert.equal(context.error, '')
  await call(context, 'load', true)
  assert.equal(probes, 1)
  assert.deepEqual(context.snapshot, { accounts: [{ id: 'saved', windows: [{ usedPercent: 45 }] }] })
  assert.equal(context.loading, false)
})

test('closing Usage rejects the cached response and does not start a probe', async () => {
  const pending = Promise.withResolvers<unknown>()
  let probes = 0
  const gate = requestGate()
  const context: Record<string, unknown> = { mounted: true, gate, loading: false, error: '', snapshot: null, openedInitial: true, accountId: '', api: { accountUsageCached: () => pending.promise, accountUsage: () => { probes++; return {} } } }
  runFunctions(component('./UsagePane.svelte'), ['load'], context)
  const loading = call(context, 'load', false)
  context.mounted = false; gate.invalidate()
  pending.resolve({ accounts: [{ id: 'saved' }] })
  await loading
  assert.equal(context.snapshot, null)
  assert.equal(probes, 0)
})
