import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { arrivals, motion, FAST, BASE, SLOW } from './motion.ts'

test('motion durations follow the shared tokens and drop to zero for reduced motion', (t) => {
  const saved = globalThis.matchMedia
  t.after(() => { globalThis.matchMedia = saved })
  globalThis.matchMedia = () => ({ matches: false })
  assert.deepEqual([FAST, BASE, SLOW].map(motion), [150, 200, 250])
  globalThis.matchMedia = (q) => ({ matches: q === '(prefers-reduced-motion: reduce)' })
  assert.deepEqual([FAST, BASE, SLOW].map(motion), [0, 0, 0])
  const css = readFileSync(new URL('../../app.css', import.meta.url), 'utf8')
  for (const [name, ms] of [['--motion-fast', FAST], ['--motion', BASE], ['--motion-slow', SLOW]]) assert.match(css, new RegExp(`${name}: ${ms}ms;`))
})

test('arrivals marks only rows added after a list first showed rows', () => {
  const isNew = arrivals()
  assert.deepEqual(['a', 'b'].filter(isNew('s1', [])), ['a', 'b'].filter(() => false), 'an empty first render sets nothing')
  assert.deepEqual(['a', 'b'].filter(isNew('s1', ['a', 'b'])), [])
  assert.deepEqual(['a', 'b', 'c'].filter(isNew('s1', ['a', 'b', 'c'])), ['c'])
  assert.deepEqual(['x', 'c'].filter(isNew('s2', ['x', 'c'])), [], 'a new scope starts over')
  assert.deepEqual(['old', 'x', 'c', 'd'].filter(isNew('s2', ['old', 'x', 'c', 'd'])), ['d'], 'earlier rows loaded above stay still')
  // The feed trims old posts, so the rows it opened with can all disappear.
  const trim = arrivals()
  trim('s', ['a', 'b'])
  assert.deepEqual(['b', 'c'].filter(trim('s', ['b', 'c'])), ['c'], 'a post arrives while the oldest is trimmed')
  assert.deepEqual(['c', 'd'].filter(trim('s', ['c', 'd'])), ['c', 'd'], 'arrivals stay marked after every first key is gone')
  assert.deepEqual(['older', 'c', 'd'].filter(trim('s', ['older', 'c', 'd'])), ['c', 'd'], 'history loaded after trimming stays still')
})

test('web motion uses shared timing tokens outside the PR view', () => {
  const root = fileURLToPath(new URL('../../', import.meta.url))
  const violations = []
  for (const file of readdirSync(root, { recursive: true })) {
    if (file.startsWith('lib/prs/') || !/\.(svelte|css)$/.test(file)) continue
    const source = readFileSync(join(root, file), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')
    const styles = file.endsWith('.css') ? [source] : [...source.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)].map((match) => match[1])
    for (const css of styles) {
      for (const [, property, value] of css.matchAll(/\b(transition(?:-duration|-delay)?|animation(?:-duration|-delay)?)\s*:\s*([^;}]+)/g)) {
        // Split animation lists without splitting cubic-bezier() or var().
        for (const item of value.split(/,(?![^()]*\))/)) {
          if (property === 'animation' && /\binfinite\b/.test(item)) continue
          const times = [...item.matchAll(/(?<![\w.-])(-?(?:\d*\.)?\d+)(ms|s)\b/g)]
          if (times.some((match) => Number(match[1]) !== 0)) violations.push(file + ': ' + property + ': ' + item.trim())
        }
      }
    }
    if (!file.endsWith('.svelte')) continue
    for (const [directive, options = ''] of source.matchAll(/\b(?:in|out|transition):[\w$]+(?:\|\w+)*(?:=\{([\s\S]*?)\}(?=[\s/>]))?/g)) {
      // A token duration, or an explicit opt-out: `<condition> ? 0 : motion(TOKEN)`.
      if (!/\bduration\s*:\s*(?:[\w.]+\s*\?\s*0\s*:\s*)?motion\(\s*(?:FAST|BASE|SLOW)\s*\)(?=\s*[,}])/.test(options)) violations.push(file + ': ' + directive)
    }
  }
  assert.deepEqual(violations, [], 'Use CSS motion tokens and motion(FAST|BASE|SLOW) for Svelte directives')
})
