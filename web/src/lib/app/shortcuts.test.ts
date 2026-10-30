import test from 'node:test'
import assert from 'node:assert/strict'
import { JSDOM } from 'jsdom'
import { createShortcut } from './shortcuts.ts'

test('Escape leaves dialog keys alone and clears selection before phone panels', () => {
  const dom = new JSDOM('<div role="dialog"><button>Close</button></div><button id="outside">Outside</button>')
  const calls: string[] = []
  const shortcut = createShortcut({
    paletteOpen: () => false, openSearch: () => calls.push('search'), closeSearch: () => calls.push('close search'),
    toggleSidebar: () => calls.push('sidebar'),
    selectingSessions: () => true, toggleSessionSelection: () => calls.push('selection'),
    drillOpen: () => true, threadsOpen: () => true, closeThreads: () => calls.push('threads'),
    viewing: () => false, cardOpen: () => false, goalOpen: () => true, closeGoal: () => calls.push('goal'),
    pinsOpen: () => true, closePins: () => calls.push('pins'), narrow: () => true,
    closePhonePanels: () => calls.push('phone panels'),
  })
  dom.window.document.addEventListener('keydown', shortcut)
  const dialogButton = dom.window.document.querySelector('div button')
  const outside = dom.window.document.querySelector('#outside')
  assert.ok(dialogButton && outside)
  dialogButton.dispatchEvent(new dom.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
  assert.deepEqual(calls, [])
  const event = new dom.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
  outside.dispatchEvent(event)
  assert.equal(event.defaultPrevented, true)
  assert.deepEqual(calls, ['selection'])
})
