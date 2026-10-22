import test from 'node:test'
import assert from 'node:assert/strict'
import { clip, actPhrase, windowPhrase, computerLine, PILL_MAX } from './summary.js'

const now = 1_000_000
const win = { state: 'running', apps: [{ appId: 'org.chromium.Chromium', title: 'Ivan Moskvitin' }] }

test('actPhrase gives each action its object', () => {
  assert.equal(actPhrase('click · 120, 340', 'Chromium'), 'Clicked in Chromium')
  assert.equal(actPhrase('double click · 1, 2', 'Chromium'), 'Double-clicked in Chromium')
  assert.equal(actPhrase('right click · 1, 2'), 'Right-clicked')
  assert.equal(actPhrase('scroll · down · 5, 9'), 'Scrolled down')
  assert.equal(actPhrase('scroll · up'), 'Scrolled up')
  assert.equal(actPhrase('scroll · 5, 9'), 'Scrolled')
  assert.equal(actPhrase('keypress · Enter'), 'Pressed Enter')
  assert.equal(actPhrase('keypress · ctrl+a'), 'Pressed Ctrl+A')
  assert.equal(actPhrase('keypress · CTRL+a'), 'Pressed Ctrl+A')
  assert.equal(actPhrase('keypress'), 'Pressed a key')
  assert.equal(actPhrase('type · "SAVE10"'), 'Typed "SAVE10"')
  assert.equal(actPhrase('type · (secret)'), 'Typed a password')
  assert.equal(actPhrase('type · 6 characters'), 'Typed 6 characters')
  assert.equal(actPhrase('type · 1 characters'), 'Typed 1 character')
  assert.equal(actPhrase('type · (hidden)'), 'Typed text')
  assert.equal(actPhrase('type'), 'Typed text')
  assert.equal(actPhrase('open · Chromium'), 'Opened Chromium')
  assert.equal(actPhrase('something odd'), '')
  assert.equal(actPhrase(''), '')
})

test('actPhrase cuts long typed text at about 24 characters', () => {
  assert.equal(actPhrase('type · "' + 'a'.repeat(24) + '…"'), 'Typed "' + 'a'.repeat(24) + '…"')
  assert.match(actPhrase('type · "' + 'abcdefghij '.repeat(6) + '"'), /^Typed "[^"]{1,25}"$/)
})

test('windowPhrase names the front window', () => {
  assert.equal(windowPhrase(win), 'In Chromium · Ivan Moskvitin')
  assert.equal(windowPhrase({ apps: [{ appId: 'foot', title: '' }] }), 'In Foot')
  assert.equal(windowPhrase({ apps: [] }), '')
  assert.equal(windowPhrase(null), '')
})

test('clip stops at a word and keeps the limit', () => {
  assert.equal(clip('short'), 'short')
  const out = clip('word '.repeat(30))
  assert.ok(out.length <= PILL_MAX && out.endsWith('…'))
  assert.ok(!out.includes('wor…'))
})

test('computerLine keeps saved status regardless of age, with window fallback only', () => {
 const c = { ...win, latestStatus: { text: 'Opening PR #869 to check Forward', at: new Date(now - 86400000).toISOString() } }
 assert.equal(computerLine({ c, now }), 'Opening PR #869 to check Forward')
 assert.equal(computerLine({ c: win }), 'In Chromium · Ivan Moskvitin')
 assert.equal(computerLine(), 'No window yet')
})
