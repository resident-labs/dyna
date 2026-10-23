import test from 'node:test'
import assert from 'node:assert/strict'
import { JSDOM } from 'jsdom'
import { documentCopy } from './documentCopy.ts'

test('code copy preserves text, handles failure, and removes old controls and pending feedback', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const dom = new JSDOM('<article><pre><code>first\n\t&lt;second&gt;\n</code></pre><pre><code></code></pre></article>')
  t.after(() => dom.window.close())
  const root = dom.window.document.querySelector('article')
  assert.ok(root)
  const copied: string[] = []
  let fail = false
  let release: (() => void) | undefined
  let pending = false
  const cleanup = documentCopy(root, async (text) => {
    if (fail) throw new Error('denied')
    copied.push(text)
    if (pending) await new Promise<void>((resolve) => { release = resolve })
  })
  const buttons = root.querySelectorAll('button')
  assert.equal(buttons.length, 2)
  buttons[0]?.click()
  await Promise.resolve()
  assert.deepEqual(copied, ['first\n\t<second>\n'])
  assert.equal(root.querySelector('[role=status]')?.textContent, 'Copied code')
  t.mock.timers.tick(2000)
  assert.equal(buttons[0]?.textContent, 'Copy code')
  fail = true
  buttons[0]?.click()
  await Promise.resolve()
  assert.equal(root.querySelector('[role=status]')?.textContent, 'Could not copy')
  fail = false
  buttons[1]?.click()
  await Promise.resolve()
  assert.equal(copied[1], '')
  pending = true
  buttons[0]?.click()
  cleanup()
  assert.ok(release)
  release()
  await Promise.resolve()
  await Promise.resolve()
  assert.equal(root.querySelectorAll('button').length, 0)
  assert.equal(root.querySelector('code')?.textContent, 'first\n\t<second>\n')
  const cleanupAgain = documentCopy(root, async () => {})
  assert.equal(root.querySelectorAll('button').length, 2)
  cleanupAgain()
})
