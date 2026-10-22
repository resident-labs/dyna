import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { call, runFunctions } from '../../../test/svelte-source.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')

test('closing shows the returned handoff until the user dismisses it', async () => {
  const moved = ['PR #7 author → root', 'Workflow wf_one canceled']
  const app = {
    confirm: { kind: 'close', m: { id: 'one', name: 'worker' } },
    closeResult: null,
    roomId: 'room',
    api: { closeSession: async () => ({ closeSummary: moved }) },
    say() {}, loadMembers() {}, loadChanges() {},
  }
  runFunctions(source, ['runConfirm'], app)
  await call(app, 'runConfirm')
  assert.deepEqual(JSON.parse(JSON.stringify(app.closeResult)), { name: 'worker', items: moved })
  assert.equal(app.confirm, null)
})
