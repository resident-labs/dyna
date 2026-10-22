import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

// A drawn graph has one plot height and one zero point. When the padding lived
// inside the drawn box, a column at the fail limit was shorter than its line and
// flex shrank the bands, so a 220 s and a 240 s run drew the same height.
const source = readFileSync(new URL('./CIHealth.svelte', import.meta.url), 'utf8')
const css = (source.match(/<style[^>]*>([\s\S]*?)<\/style>/)?.[1] ?? '').replace(/\/\*[\s\S]*?\*\//g, '')
const markup = source.slice(0, source.indexOf('<style'))

interface Rule {
  selector: string
  body: string
}

const rules: Rule[] = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].flatMap(([, selectors, body]) => {
  assert.ok(selectors !== undefined && body !== undefined)
  return selectors.trim().split(/,(?![^()]*\))/).map((selector) => ({ selector: selector.trim(), body }))
})

const bodyOf = (selector: string): string => {
  const found = rules.filter((rule) => rule.selector === selector).map((rule) => rule.body).join(';')
  assert.notEqual(found, '', `no rule for ${selector}`)
  return found
}

test('the drawn plot carries no padding, so columns and lines share a zero', () => {
  const plot = bodyOf('.plot')
  assert.ok(!/(^|;)\s*padding(-top|-bottom)?\s*:/.test(plot), 'padding inside the plot moves the zero point')
  assert.ok(/height/.test(markup), 'the plot takes its height from the drawn size')
  // The wrapper holds the room for the budget labels.
  assert.ok(/(^|;)\s*padding\s*:/.test(bodyOf('.chart')), 'the labels need their space outside the plot')
})

test('bands never shrink and the column has no gaps', () => {
  assert.ok(/(^|;)\s*flex\s*:\s*none/.test(bodyOf('.band')), 'a band must keep its assigned height')
  assert.ok(/(^|;)\s*gap\s*:\s*0/.test(bodyOf('.col')), 'a column gap would make the shares add past the column')
})

test('the lines are positioned inside the plot on the same size', () => {
  const plot = markup.slice(markup.indexOf('class="plot"'))
  assert.ok(/style:bottom="\{warnPx\}px"/.test(plot), 'the warn line sits in the plot')
  assert.ok(/style:bottom="\{failPx\}px"/.test(plot), 'the fail line sits in the plot')
})

// The error had to move above the detail branches; an error rendered only by
// the overview left an open detail silent.
test('a navigation error renders above every level', () => {
  const marker = markup.indexOf('class="err"')
  assert.ok(marker > 0, 'the error line is missing')
  const firstDetail = markup.indexOf('{#if run}')
  assert.ok(firstDetail > 0 && marker < firstDetail, 'the error line must precede the first detail branch')
})
