import test from 'node:test'
import assert from 'node:assert/strict'
import { sessionTabs, type SessionTabsInput } from './sessionTabs.ts'

const ids = (o: SessionTabsInput) => sessionTabs(o).map((t) => t.id)

const attachment = { id: 'a1', name: 'shot.png', mime: 'image/png', size: 1, sha256: 'abc' }

test('the Changes tab shows only when the session worktree has changes', () => {
  assert.deepEqual(ids({}), ['chat'])
  assert.deepEqual(ids({ changes: 0 }), ['chat'])
  assert.deepEqual(ids({ changes: 3 }), ['chat', 'changes'])
  assert.equal(sessionTabs({ changes: 3 }).find((t) => t.id === 'changes')?.count, 3)
})

test('Changes sits after Messages, Media and Computer, before Pull requests', () => {
  const media = [{ attachments: [attachment] }]
  assert.deepEqual(ids({ media, computer: { state: 'none' }, prs: [{ state: 'open' }], changes: 2 }), ['chat', 'media', 'computer', 'changes', 'prs'])
})

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
