import test from 'node:test'
import assert from 'node:assert/strict'
import { sessionTabs, type SessionTabsInput } from './sessionTabs.ts'

const ids = (o: SessionTabsInput) => sessionTabs(o).map((t) => t.id)

test('the Shell tab shows while the session runs a shell, or while it is open, and has no count', () => {
  assert.deepEqual(ids({ shells: 2 }), ['chat', 'shell'])
  assert.deepEqual(ids({ shells: 0 }), ['chat'])
  assert.deepEqual(ids({ shells: 0, tab: 'shell' }), ['chat', 'shell'])
  const shell = sessionTabs({ shells: 2 }).find((t) => t.id === 'shell')
  assert.ok(shell)
  assert.equal(shell.count, undefined)
  assert.equal(shell.aria, undefined)
})

test('the Computer tab shows its name only, with no running dot', () => {
  const computer = sessionTabs({ computer: { state: 'running' } }).find((t) => t.id === 'computer')
  assert.ok(computer)
  assert.equal(computer.live, undefined)
})
