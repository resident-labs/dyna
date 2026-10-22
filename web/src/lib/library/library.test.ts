import test from 'node:test'
import assert from 'node:assert/strict'
import type { LibraryEntry } from '../api/library.ts'
import { libraryTree, linkTarget, isStale } from './library.ts'

const e = (id: string, parent = '', extra: Partial<LibraryEntry> = {}): LibraryEntry => ({ id, name: id, kind: 'feature', parent, summary: '', owns: [], ...extra })

test('children follow their parent and retired entries never appear', () => {
  const rows = libraryTree([e('b-child', 'b'), e('a'), e('b'), e('gone', 'a', { retired: true }), e('a-child', 'a')])
  assert.deepEqual(rows.map((r) => [r.entry.id, r.depth]), [['a', 0], ['a-child', 1], ['b', 0], ['b-child', 1]])
})

test('an entry with a missing or retired parent stays at the top level', () => {
  const rows = libraryTree([e('x', 'nope'), e('old', '', { retired: true }), e('y', 'old')])
  assert.deepEqual(rows.map((r) => [r.entry.id, r.depth]), [['x', 0], ['y', 0]])
})

test('a glob opens the directory before its first wildcard, a plain link opens the file', () => {
  assert.deepEqual(linkTarget('internal/goals/**'), { path: 'internal/goals', dir: true })
  assert.deepEqual(linkTarget('web/src/*.ts'), { path: 'web/src', dir: true })
  assert.deepEqual(linkTarget('**/*.go'), { path: '', dir: true })
  assert.deepEqual(linkTarget('docs/library.md'), { path: 'docs/library.md', dir: false })
})

test('stale ids come from the view', () => {
  assert.equal(isStale({ stale: ['b'] }, 'b'), true)
  assert.equal(isStale({ stale: ['b'] }, 'a'), false)
  assert.equal(isStale(null, 'a'), false)
})
