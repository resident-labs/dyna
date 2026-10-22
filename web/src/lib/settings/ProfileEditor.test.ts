import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./ProfileEditor.svelte', import.meta.url), 'utf8')

// A step other than 1 makes the browser reject values off its grid, such as
// 95000 with min 1 and step 1000, and that blocks Save for the whole form.
test('the compact field accepts any whole token count, such as 95000', () => {
  const input = source.match(/Compact at \(tokens\)<input[^>]*>/)?.[0]
  assert.ok(input, 'compact input not found')
  assert.match(input, /type="number"/)
  assert.doesNotMatch(input, /\bstep=/)
})
