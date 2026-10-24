import { errorText } from '../errorText.ts'
import { allowsForceClose, closeRecoveryOf } from '../api/session.ts'
import test from 'node:test'
import assert from 'node:assert/strict'
import { call } from '../../../test/svelte-source.ts'
import { installConfirm } from '../../../test/appModules.ts'

test('closing shows the returned handoff until the user dismisses it', async () => {
  const moved = ['PR #7 author → root', 'Workflow wf_one canceled']
  const app = {
    confirm: { kind: 'close', m: { id: 'one', name: 'worker' } },
    closeResult: null, errorText,
    roomId: 'room',
    api: { closeSession: async () => ({ closeSummary: moved }) },
    say() {}, loadMembers() {}, loadChanges() {},
  }
  // The confirm dialog moved to lib/app/confirm.svelte.ts.
  installConfirm(app)
  await call(app, 'runConfirm')
  assert.deepEqual(JSON.parse(JSON.stringify(app.closeResult)), { name: 'worker', items: moved })
  assert.equal(app.confirm, null)
})

test('failed handoff shows typed responsibilities before offering force close', async () => {
  const items = [{ kind: 'pr', id: 'github.com/owner/repo#17', text: 'PR #17 author has no open ancestor or root' }]
  const app = {
    confirm: { kind: 'close', m: { id: 'one', name: 'worker' }, items: [] as typeof items },
    closeResult: null, errorText, allowsForceClose, closeRecoveryOf,
    roomId: 'room',
    api: { closeSession: async () => { throw { message: 'handoff failed without legacy prose', close: { reason: 'responsibilities', actions: ['force-close'], items } } } },
  }
  installConfirm(app)
  await call(app, 'runConfirm')
  assert.equal(app.confirm.kind, 'forceclose')
  assert.deepEqual([...app.confirm.items], items)
  assert.equal(app.closeResult, null)
})
