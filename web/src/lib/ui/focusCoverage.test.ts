import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'

const root = new URL('../../', import.meta.url)
test('links, disclosure controls, selects and non-text inputs have global focus rings', () => {
  const css = readFileSync(new URL('app.css', root), 'utf8')
  const rule = css.match(/([^{}]+)\{[^{}]*outline: 2px solid var\(--iris-edge\);[^{}]*\}/)?.[1]
  assert.ok(rule !== undefined, 'app.css has no global focus ring rule')
  for (const selector of ['a:focus-visible', 'summary:focus-visible', 'select:focus-visible', 'input:focus-visible']) {
    assert.ok(rule.split(',').some((part) => part.trim().endsWith(selector)), selector)
  }
})

test('Svelte focus-visible rules keep a visible focus style', () => {
  for (const entry of readdirSync(root, { recursive: true, encoding: 'utf8' })) {
    if (!entry.endsWith('.svelte') || entry.startsWith('lib/prs/')) continue
    const source = readFileSync(new URL(entry, root), 'utf8')
    const css = source.match(/<style[^>]*>([\s\S]*?)<\/style>/)?.[1] ?? ''
    for (const [, selector, body] of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      assert.ok(selector !== undefined && body !== undefined)
      if (!selector.includes(':focus-visible') || !/outline:\s*(?:none|0)\s*;/.test(body)) continue
      assert.match(body, /(?:border(?:-color)?|box-shadow|background(?:-color)?):\s*(?!none|transparent)[^;]+;/, entry + selector)
    }
  }
})
