import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

test('a merge state chip eases into its new colour and label', () => {
  const chip = readFileSync(new URL('./Chip.svelte', import.meta.url), 'utf8')
  assert.match(chip, /\.chip \{[^}]*transition: background-color var\(--motion-fast\) var\(--ease\), color var\(--motion-fast\) var\(--ease\), border-color var\(--motion-fast\) var\(--ease\)/)
})
