import test from 'node:test'
import assert from 'node:assert/strict'
import { filterLoaded } from './filterLoaded.ts'
import type { Listing } from './repo.ts'

test('loaded filtering reveals matching descendants and ancestors without inventing unopened contents', () => {
  const lists: Record<string, Listing> = {
    '': { entries: [{ name: 'src', path: 'src', kind: 'dir' }, { name: 'unopened', path: 'unopened', kind: 'dir' }, { name: 'notes.md', path: 'notes.md', kind: 'file' }] },
    src: { entries: [{ name: 'lib', path: 'src/lib', kind: 'dir' }, { name: 'other.ts', path: 'src/other.ts', kind: 'file' }] },
    'src/lib': { entries: [{ name: 'Match.ts', path: 'src/lib/Match.ts', kind: 'file' }] },
  }
  assert.deepEqual(filterLoaded(lists, '  MATCH ').map(row => [row.path, row.expanded]), [
    ['src', true], ['src/lib', true], ['src/lib/Match.ts', false],
  ])
  assert.deepEqual(filterLoaded(lists, 'missing'), [])
  assert.deepEqual(filterLoaded(lists, 'unopened').map(row => [row.path, row.expanded]), [['unopened', false]])
  assert.equal(Object.hasOwn(lists, 'unopened'), false)
})
