import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

test('the member facts panel keeps its three-column grid', () => {
  const text = readFileSync(new URL('./ProfileModal.svelte', import.meta.url), 'utf8')
  assert.match(text, /<dl class="facts">/)
  assert.match(text.slice(text.indexOf('<style>')), /\.facts \{[^}]*display: grid;[^}]*grid-template-columns: repeat\(3, 1fr\)/)
})
