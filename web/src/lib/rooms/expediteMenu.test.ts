import test from 'node:test'
import assert from 'node:assert/strict'
import { expediteMenuItem } from './expediteMenu.ts'

test('a session with no goal has no item, one goal expedites at once, several open a picker', () => {
  assert.equal(expediteMenuItem([], 'g1'), null)
  // One goal: the item names it, and stops it when it is expedited already.
  assert.deepEqual(expediteMenuItem([{ id: 'g2' }], 'g1'), { label: 'Expedite g2', mode: 'direct' })
  assert.deepEqual(expediteMenuItem([{ id: 'g1' }], 'g1'), { label: 'Stop expediting', mode: 'direct' })
  // Several goals: one picker item, with a check on the expedited goal when one is held.
  assert.deepEqual(expediteMenuItem([{ id: 'g1' }, { id: 'g2' }], 'g1'), { label: 'Expedite…', hint: '✓ g1', mode: 'choose' })
  assert.deepEqual(expediteMenuItem([{ id: 'g1' }, { id: 'g2' }], 'g9'), { label: 'Expedite…', mode: 'choose' })
})
