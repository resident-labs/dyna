import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { call, runFunctions } from '../../../test/svelte-source.ts'

test('completion links intercept only unmodified primary activation with a callback', () => {
  const opened: unknown[] = []
  const detail = { kind: 'shell', id: 'sh_20261023-010000-12345678' }
  const context: Record<string, unknown> = { post: { detail }, onopen: (value: unknown) => opened.push(value) }
  runFunctions(readFileSync(new URL('./CompletionBody.svelte', import.meta.url), 'utf8'), ['open'], context)
  let prevented = 0
  const event = (extra = {}) => ({ button: 0, preventDefault() { prevented++ }, stopPropagation() {}, ...extra })
  call(context, 'open', event())
  assert.deepEqual(opened, [detail])
  assert.equal(prevented, 1)
  for (const extra of [{ ctrlKey: true }, { metaKey: true }, { shiftKey: true }, { altKey: true }, { button: 1 }, { defaultPrevented: true }]) {
    call(context, 'open', event(extra))
  }
  context.onopen = null
  call(context, 'open', event())
  assert.equal(prevented, 1)
  assert.equal(opened.length, 1)
})
