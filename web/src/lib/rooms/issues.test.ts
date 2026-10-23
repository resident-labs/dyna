import test from 'node:test'
import assert from 'node:assert/strict'
import { areas, byArea, location, historyLabel, type IssueView } from './issues.ts'

const list: IssueView[] = [
  { id: 'i1', file: 'internal/pr/trial/build.go', line: 212 },
  { id: 'i2', file: 'web/src/lib/rooms/Pins.svelte', line: 88 },
  { id: 'i3', file: 'internal/ci/budget.go', line: 57 },
  { id: 'i4', file: '', line: 0 },
]

test('areas lists the top folders of the issue files, most used first', () => {
  assert.deepEqual(areas(list), ['internal', 'web'])
})

test('byArea keeps all issues for an empty area and filters by top folder', () => {
  assert.equal(byArea(list, '').length, 4)
  assert.deepEqual(byArea(list, 'web').map((i) => i.id), ['i2'])
})

test('location shows file:line, the file alone when the line is unknown, and nothing without a file', () => {
  assert.equal(location(list[0] ?? {}), 'internal/pr/trial/build.go:212')
  assert.equal(location({ file: 'a.go', line: 0 }), 'a.go')
  assert.equal(location(list[3] ?? {}), '')
})

test('historyLabel names how an issue left the open list', () => {
  assert.equal(historyLabel({ state: 'goal', goalId: 'g12' }), 'goal g12')
  assert.equal(historyLabel({ state: 'exported' }), 'exported')
  assert.equal(historyLabel({ state: 'closed' }), 'closed')
})
