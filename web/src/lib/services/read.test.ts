import assert from 'node:assert/strict'
import test from 'node:test'
import { readHistory, readRegistry } from './read.ts'

test('the merge history reader keeps the fields a trial entry uses', () => {
  assert.deepEqual(readHistory([{ kind: 'trial', outcome: 'passed', at: '2026-10-20T01:00:00Z', head: 'h', changes: ['a', 'b'], detail: 'go-test' }]), [{ kind: 'trial', outcome: 'passed', at: '2026-10-20T01:00:00Z', changes: 2, detail: 'go-test', branch: '', head: 'h', labels: ['a', 'b'] }])
})

test('a registry row with an empty detail is kept, and an unknown id is dropped', () => {
  const row = { id: 'updates', group: 'this-computer', label: 'Updates', description: 'Keeps Dyna current', state: 'on', detail: '', count: 0, profile: '', session: '', settingsKeys: [] }
  assert.deepEqual(readRegistry([row, { ...row, id: 'nonsense' }])?.map((r) => r.id), ['updates'])
  assert.equal(readRegistry(null), null)
})
