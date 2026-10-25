import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'

// The visuals team owns these; they move onto the scale in their own change.
const skip = new Set(['Rich.svelte', 'Files.svelte', 'CardFrame.svelte', 'Viewer.svelte', 'Visuals.svelte'])
const dirs = ['../sessions', '../rooms', '../ui', '../settings', '../settings/accounts', '../settings/harnesses']

interface Source {
  name: string
  text: string
}

function sources(): Source[] {
  const out: Source[] = [{ name: 'app.css', text: readFileSync(new URL('../../app.css', import.meta.url), 'utf8') }]
  for (const d of dirs) {
    for (const f of readdirSync(new URL(`${d}/`, import.meta.url))) {
      if (!/\.(svelte|css)$/.test(f) || skip.has(f)) continue
      out.push({ name: `${d}/${f}`, text: readFileSync(new URL(`${d}/${f}`, import.meta.url), 'utf8') })
    }
  }
  return out
}

test('font sizes use the type scale, not literal px values', () => {
  const bad: string[] = []
  for (const { name, text } of sources()) {
    for (const m of text.matchAll(/font(?:-size)?:\s*[^;"}]*?\b\d*\.?\d+px/g)) bad.push(`${name}: ${m[0]}`)
  }
  assert.deepEqual(bad, [])
})

test('every type token a file uses is defined in tokens.css', () => {
  const tokens = readFileSync(new URL('./tokens.css', import.meta.url), 'utf8')
  const defined = new Set<string>()
  for (const [, token] of tokens.matchAll(/(--(?:t|leading)[\w-]*):/g)) {
    assert.ok(token !== undefined)
    defined.add(token)
  }
  const missing = new Set<string>()
  for (const { text } of sources()) {
    for (const [, token] of text.matchAll(/var\((--(?:t|leading)[\w-]*)\)/g)) {
      assert.ok(token !== undefined)
      if (!defined.has(token)) missing.add(token)
    }
  }
  assert.deepEqual([...missing], [])
})
