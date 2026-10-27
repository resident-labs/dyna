import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { terminalFocusHint, terminalFocusOnOpen, terminalTypeHint, terminalReady } from './terminalFocus.ts'

test('a terminal that failed to attach is not ready, so it shows no hint', () => {
  assert.equal(terminalReady({ attached: true, failed: false, exited: false }), true)
  assert.equal(terminalReady({ attached: false, failed: false, exited: false }), false)
  assert.equal(terminalReady({ attached: true, failed: true, exited: false }), false)
  const ready = terminalReady({ attached: true, failed: true, exited: false })
  assert.equal(terminalFocusHint({ ready, focused: false }), false)
})

// api.termAttach starts its stream and returns at once; a missing terminal or a
// refused attachment arrives later as an exit event. Readiness must follow that
// event, or the pane advertises typing on a terminal that cannot take it.
test('an attach that fails after the pane is ready stops being ready', () => {
  const opened = { attached: true, failed: false, exited: false }
  assert.equal(terminalReady(opened), true)
  assert.equal(terminalFocusHint({ ready: terminalReady(opened), focused: false }), true)
  const gone = { ...opened, exited: true }
  assert.equal(terminalReady(gone), false)
  assert.equal(terminalFocusHint({ ready: terminalReady(gone), focused: false }), false)
})

test('the hint appears once the terminal is ready and typing would go nowhere', () => {
  // Before the terminal is on screen there is nothing to click, so no hint.
  assert.equal(terminalFocusHint({ ready: false, focused: false }), false)
  assert.equal(terminalFocusHint({ ready: true, focused: false }), true)
  assert.equal(terminalFocusHint({ ready: true, focused: true }), false)
})

test('a pane takes focus on open unless it is a phone pane or refuses it', () => {
  // A desktop session pane focuses itself, so typing works at once.
  assert.equal(terminalFocusOnOpen(null, false), true)
  // A phone pane keeps the soft keyboard down.
  assert.equal(terminalFocusOnOpen(null, true), false)
  // An explicit override can focus a narrow pane.
  assert.equal(terminalFocusOnOpen(true, true), true)
  assert.equal(terminalFocusOnOpen(false, false), false)
})

test('the terminal pane shows a focus hint', () => {
  const pane = readFileSync(new URL('./TermPane.svelte', import.meta.url), 'utf8')
  assert.match(pane, /terminalFocusHint\(/, 'the pane shows the hint while it lacks focus')
  assert.match(pane, /terminalReady\(/, 'the pane shows the hint only for a terminal that attached')
  assert.match(pane, /terminalFocusOnOpen\(/, 'the pane decides focus on open from its props')
  assert.match(pane, /addEventListener\('focus'/, 'the pane follows the terminal input focus')
  assert.match(pane, /addEventListener\('blur'/, 'the pane follows the terminal input blur')
  assert.match(pane, /terminalTypeHint\(/, 'the hint text follows the pointer')
  // Readiness is derived from attached, failed and exited, so no later
  // assignment can revive it after the attach stream reported an exit.
  assert.match(pane, /exited = true/, 'an exit event marks the terminal gone')
  assert.doesNotMatch(pane, /^\s*ready = /m, 'readiness is derived, never assigned')
})

test('the focus hint says tap on a touch screen and click with a mouse', () => {
  assert.equal(terminalTypeHint(true), 'Tap to type')
  assert.equal(terminalTypeHint(false), 'Click to type')
})
