import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { requestGate } from './state.ts'
import { call, runFunctions } from '../../../../test/svelte-source.ts'

const component = (file: string): string => readFileSync(new URL(file, import.meta.url), 'utf8')

test('profile handoffs are one-shot and normal navigation cannot replay them', () => {
  const context: Record<string, unknown> = { pane: 'profiles', profileSelection: { harness: 'pi' }, profileToOpen: 'old', accountToOpen: '' }
  runFunctions(component('../Settings.svelte'), ['selectPane', 'viewAccount', 'viewProfile'], context)
  call(context, 'viewAccount', 'account-a')
  assert.equal(context.profileSelection, null); assert.equal(context.profileToOpen, ''); assert.equal(context.pane, 'accounts')
  call(context, 'viewProfile', 'reviewer')
  assert.equal(context.pane, 'profiles'); assert.equal(context.profileToOpen, 'reviewer')
  call(context, 'selectPane', 'accounts'); call(context, 'selectPane', 'profiles')
  assert.equal(context.profileToOpen, ''); assert.equal(context.profileSelection, null)
})

test('profile creation requires the account in this scope and binds the saved account', () => {
  const selections: unknown[] = []
  const account = { id: 'saved-a', harness: 'pi', routeId: 'anthropic/sonnet', activeHere: false }
  const context: Record<string, unknown> = { account, oncreateprofile: (selection: unknown) => selections.push(JSON.parse(JSON.stringify(selection))) }
  runFunctions(component('./AccountDetails.svelte'), ['createProfile'], context)
  call(context, 'createProfile'); assert.equal(selections.length, 0)
  account.activeHere = true; call(context, 'createProfile')
  assert.deepEqual(selections, [{ harness: 'pi', routeId: 'anthropic/sonnet', connectionId: 'saved-a' }])
})

test('signing in from details closes the drawer and opens the sign-in form', () => {
  const context: Record<string, unknown> = { detail: { account: { id: 'saved-a' } }, signInFor: null, accountLabel: () => 'Work' }
  runFunctions(component('./AccountsPane.svelte'), ['startSignIn'], context)
  call(context, 'startSignIn', { id: 'saved-a' })
  assert.equal(context.detail, null)
  assert.deepEqual(JSON.parse(JSON.stringify(context.signInFor)), { id: 'saved-a', label: 'Work' })
})

test('the account page reads saved observations until an explicit provider refresh', async () => {
  const seen: [string, boolean][] = []
  const context: Record<string, unknown> = {
    mounted: true, gate: requestGate(), loading: false, error: '', snapshot: null, openedInitial: true, accountId: '', scope: '',
    api: {
      accountsView: async (scope: string) => {
        seen.push([scope, false])
        return { providers: [{ provider: 'zai', accounts: [{ id: 'saved', windows: [{ usedPercent: 40 }] }] }], problems: [], checkedAt: '2026-10-25T12:00:00Z' }
      },
      accountsList: async (scope: string, refresh: boolean) => {
        seen.push([scope, refresh])
        return { providers: [{ provider: 'zai', accounts: [{ id: 'saved', windows: [{ usedPercent: 45 }] }] }], problems: [], checkedAt: '2026-10-25T12:00:00Z' }
      },
    },
  }
  runFunctions(component('./AccountsPane.svelte'), ['load'], context)
  await call(context, 'load', false)
  assert.deepEqual(seen, [['', false]], 'the first read takes the saved view')
  assert.equal(context.error, '')
  await call(context, 'load', true)
  assert.deepEqual(seen.at(-1), ['', true], 'Refresh asks the provider')
  assert.equal(context.loading, false)
})

test('closing the page rejects a delayed response and does not start a probe', async () => {
  const pending = Promise.withResolvers<unknown>()
  let probes = 0
  const gate = requestGate()
  const context: Record<string, unknown> = { mounted: true, gate, loading: false, error: '', snapshot: null, openedInitial: true, accountId: '', scope: '', api: { accountsView: () => { probes++; return pending.promise } } }
  runFunctions(component('./AccountsPane.svelte'), ['load'], context)
  const loading = call(context, 'load', false)
  context.mounted = false; gate.invalidate()
  pending.resolve({ providers: [], problems: [], checkedAt: '' })
  await loading
  assert.equal(context.snapshot, null)
  assert.equal(probes, 1)
})
