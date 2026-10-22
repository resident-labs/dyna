import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

test('the session view passes the count of its one visible stream', async () => {
  const app = await readFile(new URL('../../App.svelte', import.meta.url), 'utf8')
  assert.ok(/chat=\{\{\s*computerCount:\s*1\s*,/.test(app), 'room membership must not control the visible-stream count')
})
