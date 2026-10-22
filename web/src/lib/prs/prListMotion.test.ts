import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

// A PR that changes stage leaves one keyed list and enters another (a group
// or the merge queue). One crossfade pair, keyed by changeId, carries it
// across; flip moves the rows that stay.
test('PR rows move between the merge queue and the stage groups instead of jumping', () => {
  const source = readFileSync(new URL('./PRList.svelte', import.meta.url), 'utf8')
  assert.match(source, /const \[send, receive\] = crossfade\(/)
  const esc = (x: string): string => x.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  for (const key of ['q.row.changeId', 'c.changeId']) {
    const slot = new RegExp(`animate:flip=\\{\\{ duration: motion\\(BASE\\)[^}]*\\}\\} in:receive\\|global=\\{\\{ key: ${esc(key)}, duration: motion\\(BASE\\) \\}\\} out:send\\|global=\\{\\{ key: ${esc(key)}, duration: motion\\(BASE\\) \\}\\}`)
    assert.match(source, slot, key)
  }
})

test('a merge state chip eases into its new colour and label', () => {
  const chip = readFileSync(new URL('../ui/Chip.svelte', import.meta.url), 'utf8')
  assert.match(chip, /\.chip \{[^}]*transition: background-color var\(--motion-fast\) var\(--ease\), color var\(--motion-fast\) var\(--ease\), border-color var\(--motion-fast\) var\(--ease\)/)
  const prChip = readFileSync(new URL('./PRChip.svelte', import.meta.url), 'utf8')
  assert.match(prChip, /\{#key meta\.label\}<span class="label" in:fade=\{\{ duration: motion\(FAST\) \}\}>\{meta\.label\}<\/span>\{\/key\}/)
})

test('the merge queue card slides in when the first PR enters and out when the last leaves', () => {
  const source = readFileSync(new URL('./PRList.svelte', import.meta.url), 'utf8')
  assert.match(source, /<div class="queue" transition:slide=\{\{ duration: motion\(BASE\), easing: cubicOut \}\}>/)
})
