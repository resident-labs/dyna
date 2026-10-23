import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { itemCountLabel } from './goals.ts'

// Goals no longer carry keeper fields, so no goal view may read them.
test('goal views never read keeper fields', () => {
  for (const file of ['./GoalDetail.svelte', './Goals.svelte', './GoalGlyph.svelte', './goals.ts', '../settings/ProfileModal.svelte']) {
    const source = readFileSync(new URL(file, import.meta.url), 'utf8')
    assert.doesNotMatch(source, /\bg(oal)?\.keeper\b|\bg(oal)?\?\.keeper\b/, file)
  }
})

// The web goal views show the checklist, the mechanical status and events.
test('goal views render items, status and events', () => {
  const read = (file: string) => readFileSync(new URL(file, import.meta.url), 'utf8')
  const detail = read('./GoalDetail.svelte')
  assert.match(detail, /checklist/, 'GoalDetail names a checklist region')
  assert.match(detail, /status\.label|statusLabel/, 'GoalDetail shows the status label')
  assert.match(detail, /reason/, 'GoalDetail shows the status reason')
  const list = read('./Goals.svelte')
  assert.match(list, /itemCounts|item counts|items\./, 'Goals list shows item counts')
  const journal = read('./GoalJournal.svelte')
  assert.match(journal, /event/, 'GoalJournal renders events')
})

test('item count labels show open and total', () => {
  assert.equal(itemCountLabel([2, 5]), '2 open · 5 items')
  assert.equal(itemCountLabel([0, 3]), '0 open · 3 items')
  assert.equal(itemCountLabel([3, 3]), '3 open · 3 items')
  assert.equal(itemCountLabel(), '')
  assert.equal(itemCountLabel([0, 0]), '')
})
