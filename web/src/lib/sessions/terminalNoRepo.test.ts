import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { call, runFunctions } from '../../../test/svelte-source.ts'

// Run the real open and shortcut code: no path may ask the host for a room
// terminal in a room without a repository.
test('no shortcut or action opens a room terminal without a repository', async () => {
  const source = readFileSync(new URL('./TermDock.svelte', import.meta.url), 'utf8')
  const calls: unknown[][] = []
  const c: Record<string, unknown> = { roomless: true, opening: false, narrow: false, ref: 'room_x', roomId: 'room_x', all: [], list: [], active: '', error: '', current: null, load() {}, askClose() {}, act() {}, dock: { open: true }, rootEl: { contains: () => true }, Node: class {},
    api: { termOpen: async (...args: unknown[]) => { calls.push(args); return { id: 't1' } } } }
  runFunctions(source, ['open', 'shortcut'], c)
  call(c, 'shortcut', { ctrlKey: true, shiftKey: true, key: 'T', target: new (c.Node as new () => object)(), preventDefault() {}, stopPropagation() {} })
  await call(c, 'open')
  assert.deepEqual(calls, [])
  assert.equal(c.error, '')
  assert.match(source, /\{#if !roomless\}<button class="primary"[^\n]*?>Start a new shell<\/button>\{\/if\}/, 'the restart action is hidden without a repository')
})
