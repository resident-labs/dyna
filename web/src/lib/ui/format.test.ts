import test from 'node:test'
import assert from 'node:assert/strict'
import DOMPurify from 'dompurify'

// Node has no browser DOM. These fakes give format.ts the two DOM calls it
// makes while it renders text: a parser and a sanitizer.
class FakeDOMParser {
  parseFromString(html: string): unknown {
    return { body: { firstChild: { innerHTML: html.replace(/^<div>|<\/div>$/g, '') }, querySelectorAll: () => [] }, createTreeWalker: () => ({ nextNode: () => false }) }
  }
}

test('markdown renders each text once and reuses the result', async (t) => {
  // Node has no browser DOM: count the mention pass instead of running it.
  let parses = 0
  const saved = { DOMParser: globalThis.DOMParser, NodeFilter: globalThis.NodeFilter, sanitize: DOMPurify.sanitize }
  t.after(() => {
    Object.assign(globalThis, { DOMParser: saved.DOMParser, NodeFilter: saved.NodeFilter })
    Object.assign(DOMPurify, { sanitize: saved.sanitize })
  })
  class CountingParser extends FakeDOMParser {
    override parseFromString(html: string): unknown {
      parses++
      return super.parseFromString(html)
    }
  }
  Object.assign(globalThis, { NodeFilter: { SHOW_TEXT: 4 }, DOMParser: CountingParser })
  Object.assign(DOMPurify, { sanitize: (html: string) => html })
  const { markdown } = await import('./format.ts')

  const first = markdown('**cached** post')
  assert.equal(markdown('**cached** post'), first)
  assert.match(first, /<strong>cached<\/strong>/)
  assert.equal(parses, 1)
  markdown('another post')
  assert.equal(parses, 2)
})

test('Dyna app links stay in the current window and external links do not', async () => {
  const { internalLinkHash } = await import('./format.ts')
  const current = { origin: 'https://dyna.test', pathname: '/' }
  assert.equal(internalLinkHash('#room=room_1&view=issues', current), '#room=room_1&view=issues')
  assert.equal(internalLinkHash('https://dyna.test/#room=room_1&view=issues', current), '#room=room_1&view=issues')
  assert.equal(internalLinkHash('https://dyna.test/#room=room_1&session=ses_1', current), '#room=room_1&session=ses_1')
  assert.equal(internalLinkHash('https://dyna.test/', current), '')
  assert.equal(internalLinkHash('https://elsewhere.test/#room=room_1', current), null)
  assert.equal(internalLinkHash('https://dyna.test/attachments/room_1/file_1', current), null, 'files are not app routes')
})

test('a folded result line drops the turn lead-in and folds whitespace', async () => {
  const { resultLine } = await import('./format.ts')
  assert.equal(resultLine('dyna-agent:ui-guy-right turn 464 completed. Result: No action needed.\n\n#866 is ready.'), 'No action needed. #866 is ready.')
  assert.equal(resultLine('Plain   text'), 'Plain text')
})

test('elapsed reads like a stopwatch, and calls over 30 s are long', async () => {
  const { elapsed, isLongCall } = await import('./format.ts')
  assert.equal(elapsed(8000), '8s')
  assert.equal(elapsed(65000), '1m 05s')
  assert.equal(elapsed(3720000), '1h 02m')
  assert.equal(isLongCall(30000), false)
  assert.equal(isLongCall(30001), true)
})

test('durationMs reads a finished span down to its milliseconds', async () => {
  const { durationMs } = await import('./format.ts')
  assert.equal(durationMs(0), '')
  assert.equal(durationMs(250), '250 ms')
  assert.equal(durationMs(12_300), '12.3 s')
  assert.equal(durationMs(207_000), '3m 27s')
})

// A schedule shows when the next run is, so a hook row reads a future time
// with the same words as a past one.
test('until reads a future time from now', async (t) => {
  // Freeze the clock: until samples Date.now itself, so a millisecond between
  // the two calls must not shorten "in 3d" to "in 2d".
  t.mock.timers.enable({ apis: ['Date'] })
  t.mock.timers.setTime(1_700_000_000_000)
  const now = Date.now()
  const { until } = await import('./format.ts')
  assert.equal(until(NaN), '')
  assert.equal(until(now - 60_000), 'in under a minute')
  assert.equal(until(now + 5 * 60_000), 'in 5m')
  assert.equal(until(now + 2 * 3_600_000), 'in 2h')
  assert.equal(until(now + 3 * 86_400_000), 'in 3d')
})
