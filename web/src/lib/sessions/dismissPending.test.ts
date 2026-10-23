import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { call, runFunctions } from '../../../test/svelte-source.ts'
import { installRouterShim } from '../../../test/appRouterShim.ts'
import { installSessionView } from '../../../test/appModules.ts'

// The real function from a component, run against a plain context.
function run(path: string, name: string, context: Record<string, unknown>): void {
  runFunctions(readFileSync(new URL(path, import.meta.url), 'utf8'), [name], context)
}

test('dismissal uses the confirmed session even if the visible session changes', async () => {
  const calls: unknown[][] = []
  const c: Record<string, unknown> = { dismissConfirm: { post: { id: 'msg_old' }, session: 'old' }, m: { id: 'new' }, ondismiss: async (...args: unknown[]) => calls.push(args), removing: '', queueError: '' }
  run('./Session.svelte', 'dismissPending', c)
  assert.equal(calls.length, 0)
  await call(c, 'dismissPending')
  assert.deepEqual(calls, [['old', 'msg_old']])
  assert.equal(c.dismissConfirm, null)
})

test('rejected active delivery keeps the confirmation and displays its error', async () => {
  const c: Record<string, unknown> = { dismissConfirm: { post: { id: 'msg_old' }, session: 'old' }, ondismiss: async () => { throw new Error('active delivery') }, removing: '', queueError: '', errorText: (e: unknown) => (e instanceof Error ? e.message : String(e)) }
  run('./Session.svelte', 'dismissPending', c)
  await call(c, 'dismissPending')
  assert.equal(c.queueError, 'active delivery')
  assert.deepEqual(c.dismissConfirm, { post: { id: 'msg_old' }, session: 'old' })
  assert.equal(c.removing, '')
})

test('app sends explicit confirmation and preserves the new session queue', async () => {
  const calls: unknown[][] = []
  // The pending post lives in lib/app/sessionView.svelte.ts now.
  const c: Record<string, unknown> = { api: { dismissPendingPost: async (...args: unknown[]) => calls.push(args) }, drill: { id: 'new' }, queue: [{ id: 'msg_new' }], say() {} }
  installRouterShim(c)
  installSessionView(c)
  await (c.dismissPendingPost as (id: string, messageId: string) => Promise<void>)('old', 'msg_old')
  assert.deepEqual(calls, [['old', 'msg_old', true]])
  assert.deepEqual(c.queue, [{ id: 'msg_new' }])
})
