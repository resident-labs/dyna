import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

// A tab switch fades the new panel in and eases the underline, instead of
// swapping the page at once.
test('PR tabs fade their panel in and ease the selected underline', () => {
  const source = readFileSync(new URL('./PRDetail.svelte', import.meta.url), 'utf8')
  assert.match(source, /\{#key tab\}<div class="tabpanel">/, 'every tab change remounts a fading panel')
  assert.match(source, /<div class="tabpanel" hidden=\{tab !== 'files'\}><PRFiles/, 'the kept Files panel fades when it shows again')
  assert.match(source, /\.tabpanel \{ animation: tabfade var\(--motion-fast\) var\(--ease\); \}/)
  assert.match(source, /\.tabs button \{[^}]*transition: color var\(--motion-fast\) var\(--ease\), border-color var\(--motion-fast\) var\(--ease\)/)
})
