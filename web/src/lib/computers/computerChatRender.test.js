import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createTestServer } from '../../../test/ssr-server.ts'

test('ComputerChat uses DmRows and the shared composer, with no renderer of its own', () => {
  const src = readFileSync(new URL('./ComputerChat.svelte', import.meta.url), 'utf8')
  assert.match(src, /import DmRows /)
  assert.match(src, /import MessageInput /)
  assert.doesNotMatch(src, /actLines|class="act"/)
})
