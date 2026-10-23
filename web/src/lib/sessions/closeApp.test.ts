import { errorText } from '../errorText.ts'
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { call, runFunctions } from '../../../test/svelte-source.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')

test('closing shows the returned handoff until the user dismisses it', async () => {
  const moved = ['PR #7 author → root', 'Workflow wf_one canceled']
  const app = {
    confirm: { kind: 'close', m: { id: 'one', name: 'worker' } },
    closeResult: null, errorText,
    roomId: 'room',
    api: { closeSession: async () => ({ closeSummary: moved }) },
    say() {}, loadMembers() {}, loadChanges() {},
  }
  runFunctions(source, ['runConfirm'], app)
  await call(app, 'runConfirm')
  assert.deepEqual(JSON.parse(JSON.stringify(app.closeResult)), { name: 'worker', items: moved })
  assert.equal(app.confirm, null)
})

test('failed handoff shows the inline cause before offering force close', async () => {
  const reason = 'responsibilities: cannot close: PR #17 author has no open ancestor or root'
  const app = {
    confirm: { kind: 'close', m: { id: 'one', name: 'worker' }, items: [] as string[] },
    closeResult: null, errorText,
    roomId: 'room',
    api: { closeSession: async () => { throw new Error(reason) } },
  }
  runFunctions(source, ['runConfirm', 'guardItems'], app)
  await call(app, 'runConfirm')
  assert.equal(app.confirm.kind, 'forceclose')
  assert.deepEqual([...app.confirm.items], [reason])
  assert.equal(app.closeResult, null)
})
