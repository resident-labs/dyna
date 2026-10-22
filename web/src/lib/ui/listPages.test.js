import test from 'node:test'
import assert from 'node:assert/strict'
import { mergeListPage } from './listPages.js'

test('a current-page refresh preserves loaded history and its continuation', () => {
  const first = mergeListPage(null, { items: [{ id: 'active', state: 'running' }], older: true, cursor: 'first' })
  const expanded = mergeListPage(first, { items: [{ id: 'old', state: 'exited' }], older: true, cursor: 'second' }, true)
  const refreshed = mergeListPage(expanded, { items: [{ id: 'new' }, { id: 'active', state: 'exited' }], older: true, cursor: 'different-first' })
  assert.deepEqual(refreshed.items.map((item) => item.id), ['new', 'active', 'old'])
  assert.equal(refreshed.items[1].state, 'exited')
  assert.equal(refreshed.cursor, 'second')
  assert.equal(refreshed.older, true)
})

test('a refresh before loading history replaces the first page and cursor', () => {
  const first = mergeListPage(null, { items: [{ key: 'expired' }], older: true, cursor: 'first' })
  const refreshed = mergeListPage(first, { items: [{ key: 'current' }], older: false, cursor: '' })
  assert.deepEqual(refreshed.items.map((item) => item.key), ['current'])
  assert.equal(refreshed.older, false)
  assert.equal(refreshed.cursor, '')
})
