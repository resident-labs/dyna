import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('../../', import.meta.url))

interface Rule {
  selector: string
  body: string
}

// These source checks cover component styles without a browser or a layout snapshot.
function styles(): { file: string; rules: Rule[] }[] {
  return readdirSync(root, { recursive: true, encoding: 'utf8' })
    .filter(file => !file.startsWith('lib/prs/') && (file.endsWith('.svelte') || file === 'app.css'))
    .map(file => {
      const source = readFileSync(join(root, file), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')
      const css = file.endsWith('.css') ? source : source.match(/<style[^>]*>([\s\S]*?)<\/style>/)?.[1] ?? ''
      const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].flatMap(([, selectors, body]) => {
        assert.ok(selectors !== undefined && body !== undefined)
        return selectors.trim().split(/,(?![^()]*\))/).map(selector => ({ selector: selector.trim(), body }))
      })
      return { file, rules }
    })
}

// A selector such as `.a.b` always has class names here; the callers check that first.
function classNames(selector: string): string[] {
  const names = selector.match(/\.[\w-]+/g)
  assert.ok(names !== null)
  return names
}

test('hover backgrounds use palette tokens', () => {
  const violations: string[] = []
  for (const { file, rules } of styles()) {
    for (const { selector, body } of rules) {
      if (!selector.includes(':hover')) continue
      for (const [, value] of body.matchAll(/(?:^|;)\s*background(?:-color)?\s*:\s*([^;]+)/g)) {
        assert.ok(value !== undefined)
        if (/#|rgba?\(|hsla?\(/.test(value)) violations.push(`${file}: ${selector}: ${value}`)
      }
    }
  }
  assert.deepEqual(violations, [], 'Use palette tokens for hover backgrounds')
})

test('hover colors transition on the element in both directions', () => {
  const violations: string[] = []
  for (const { file, rules } of styles()) {
    for (const { selector, body } of rules) {
      if (!selector.includes(':hover')) continue
      const base = selector.replace(/:hover/g, '')
      // Simple class variants and descendant subjects inherit their base rule.
      const subject = base.split(/\s+(?![^()]*\))/).at(-1)
      assert.ok(subject !== undefined)
      const declarations = rules.filter(rule => {
        if (rule.selector === base || rule.selector === subject) return true
        return /^(\.[\w-]+)+$/.test(subject) && /^(\.[\w-]+)+$/.test(rule.selector)
          && classNames(rule.selector).every(name => classNames(subject).includes(name))
      }).map(rule => rule.body).join(';')
      for (const [, property] of body.matchAll(/(?:^|;)\s*(background(?:-color)?|color)\s*:/g)) {
        assert.ok(property !== undefined)
        const name = property.startsWith('background') ? 'background-color' : 'color'
        if (!new RegExp(`(?:^|[;,])\\s*${name}\\s+var\\(--motion-fast\\)`).test(declarations.replace(/transition\s*:/g, ';'))) {
          violations.push(`${file}: ${selector}: ${name}`)
        }
      }
    }
  }
  assert.deepEqual(violations, [], 'Put shared fast color transitions on the base element')
})

test('pressed surface styles win over the same pointer hover selector', () => {
  const violations: string[] = []
  for (const { file, rules } of styles()) {
    for (const [index, rule] of rules.entries()) {
      if (!rule.selector.includes(':hover') || !/(?:^|;)\s*background(?:-color)?\s*:/.test(rule.body)) continue
      if (/:hover\s+\S/.test(rule.selector) || rule.selector.includes(':global(') || file === 'app.css') continue
      const active = rule.selector.replace(':hover', ':active')
      const pressed = rules.findLastIndex(candidate => candidate.selector === active)
      if (pressed <= index) violations.push(`${file}: ${active}`)
    }
  }
  assert.deepEqual(violations, [], 'Declare pressed surface feedback after pointer hover feedback')
})

test('a pressed dark surface keeps its label readable', () => {
  const violations: string[] = []
  for (const { file, rules } of styles()) {
    // A label in the dark ink color (#18171a) sits on a light button; when
    // pressing turns that button dark, the pressed rule must change the label.
    const dark = rules.filter(({ selector, body }) => /^\.[\w-]+$/.test(selector) && /(^|[;\s])color:\s*#18171a/.test(body)).map(({ selector }) => selector)
    for (const { selector, body } of rules) {
      const base = selector.replace(/:active$/, '')
      if (base === selector || !dark.includes(base)) continue
      if (/background[^;]*var\(--select\)/.test(body) && !/(^|[;\s])color:\s*var\(--ink\)/.test(body)) violations.push(`${file}: ${selector}`)
    }
  }
  assert.deepEqual(violations, [], 'Give pressed dark buttons a light label')
})
