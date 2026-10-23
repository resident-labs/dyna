import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

test('a merge state chip eases into its new colour and label', () => {
  const chip = readFileSync(new URL('../ui/Chip.svelte', import.meta.url), 'utf8')
  assert.match(chip, /\.chip \{[^}]*transition: background-color var\(--motion-fast\) var\(--ease\), color var\(--motion-fast\) var\(--ease\), border-color var\(--motion-fast\) var\(--ease\)/)
  const prChip = readFileSync(new URL('./PRChip.svelte', import.meta.url), 'utf8')
  assert.match(prChip, /\{#key meta\.label\}<span class="label" in:fade=\{\{ duration: motion\(FAST\) \}\}>\{meta\.label\}<\/span>\{\/key\}/)
})
