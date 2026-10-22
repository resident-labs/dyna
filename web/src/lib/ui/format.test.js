import test from 'node:test'
import assert from 'node:assert/strict'
import DOMPurify from 'dompurify'

test('markdown renders each text once and reuses the result', async (t) => {
  // Node has no browser DOM: count the mention pass instead of running it.
  let parses = 0
  const saved = [globalThis.DOMParser, globalThis.NodeFilter, DOMPurify.sanitize]
  globalThis.NodeFilter = { SHOW_TEXT: 4 }
  globalThis.DOMParser = class {
    parseFromString(html) {
      parses++
      return { body: { firstChild: { innerHTML: html.replace(/^<div>|<\/div>$/g, '') } }, createTreeWalker: () => ({ nextNode: () => false }) }
    }
  }
  DOMPurify.sanitize = (html) => html
  t.after(() => { [globalThis.DOMParser, globalThis.NodeFilter, DOMPurify.sanitize] = saved })
  const { markdown } = await import('./format.js')

  const first = markdown('**cached** post')
  assert.equal(markdown('**cached** post'), first)
  assert.match(first, /<strong>cached<\/strong>/)
  assert.equal(parses, 1)
  markdown('another post')
  assert.equal(parses, 2)
})

test('a folded result line drops the turn lead-in and folds whitespace', async () => {
  const { resultLine } = await import('./format.js')
  assert.equal(resultLine('dyna-agent:ui-guy-right turn 464 completed. Result: No action needed.\n\n#866 is ready.'), 'No action needed. #866 is ready.')
  assert.equal(resultLine('Plain   text'), 'Plain text')
})

test('elapsed reads like a stopwatch, and calls over 30 s are long', async () => {
  const { elapsed, isLongCall } = await import('./format.js')
  assert.equal(elapsed(8000), '8s')
  assert.equal(elapsed(65000), '1m 05s')
  assert.equal(elapsed(3720000), '1h 02m')
  assert.equal(isLongCall(30000), false)
  assert.equal(isLongCall(30001), true)
})
