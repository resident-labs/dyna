import { errorText } from '../errorText.ts'
import { allowsForceClose, closeRecoveryOf } from '../api/session.ts'
import test from 'node:test'
import assert from 'node:assert/strict'
import { call } from '../../../test/svelte-source.ts'
import { installConfirm } from '../../../test/appModules.ts'

test('closing shows the returned handoff until the user dismisses it', async () => {
  const moved = [
    { kind: 'pr', result: 'moved', number: 7, to: 'dyna:root' },
    { kind: 'workflow', result: 'canceled', id: 'wf_one' },
  ]
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
  assert.deepEqual(JSON.parse(JSON.stringify(app.closeResult)), { name: 'worker', outcomes: moved })
  assert.equal(app.confirm, null)
})

test('failed handoff shows typed responsibilities before offering force close', async () => {
  const detail = 'handoff failed without legacy prose'
  const itemText = 'PR #17 author has no open ancestor or root'
  const items = [{ kind: 'pr', id: 'github.com/owner/repo#17', text: itemText }]
  const app = {
    confirm: { kind: 'close', m: { id: 'one', name: 'worker' }, items: [] as typeof items },
    closeResult: null, errorText, allowsForceClose, closeRecoveryOf,
    roomId: 'room',
    api: { closeSession: async () => { throw { message: detail, close: { reason: 'responsibilities', actions: ['force-close'], items } } } },
  }
  installConfirm(app)
  await call(app, 'runConfirm')
  assert.equal(app.confirm.kind, 'forceclose')
  assert.deepEqual([...app.confirm.items], items)
  assert.equal(Reflect.get(app.confirm, 'detail'), detail)
  assert.deepEqual(JSON.parse(JSON.stringify(call(app, 'forceCloseLines', app.confirm.items, Reflect.get(app.confirm, 'detail')))), [
    detail,
    itemText,
    'Retry after fixing the error, or close anyway. The work above stays unresolved.',
  ])
  assert.equal(app.closeResult, null)
})

// Root and the owner hold no goals, so the close offers no handoff that would
// give them one: the only choice is the close the server performs, and force
// close is offered only after the server returns that recovery.
test('the close dialog offers only the choice the server performs', () => {
  const app = {
    confirm: { kind: 'close', m: { id: 'one', name: 'worker' } },
    closeResult: null, errorText, allowsForceClose, closeRecoveryOf,
    roomId: 'room', members: [],
    api: { closeSession: async () => ({ closeSummary: [] }) },
    say() {}, loadMembers() {}, loadChanges() {},
  }
  installConfirm(app)
  const text = call(app, 'closeConfirm', { id: 'one', name: 'worker' }, false) as { lines: string[]; action: string; alternative?: string; onalternative?: unknown }
  assert.equal(text.action, 'Close session')
  assert.equal(text.alternative, undefined)
  assert.equal(text.onalternative, undefined)
})
