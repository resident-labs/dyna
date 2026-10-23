import test from 'node:test'
import assert from 'node:assert/strict'
import { JSDOM } from 'jsdom'
import { phoneKeyboard, type PhoneKeyboardOptions } from './phoneKeyboard.ts'

type Sent = Parameters<PhoneKeyboardOptions['sendKey']>
type Win = JSDOM['window']

interface Harness {
  node: HTMLTextAreaElement
  sent: Sent[]
  keyboard: ReturnType<typeof phoneKeyboard>
  event: (name: string) => void
  input: (value: string, options?: InputEventInit) => void
  window: Win
}

// textarea finds the test page's textarea and checks its type before use.
function textarea(dom: JSDOM): HTMLTextAreaElement {
  const node = dom.window.document.querySelector('textarea')
  if (!(node instanceof dom.window.HTMLTextAreaElement)) throw new TypeError('the test page has no textarea')
  return node
}

function setup(run: (h: Harness) => void): void {
  const dom = new JSDOM('<textarea></textarea>')
  const node = textarea(dom)
  const sent: Sent[] = []
  const keyboard = phoneKeyboard(node, { sendKey: (...args) => sent.push(args) })
  const event = (name: string) => node.dispatchEvent(new dom.window.Event(name, { bubbles: true, cancelable: true }))
  const input = (value: string, options?: InputEventInit) => { node.value = value; node.dispatchEvent(new dom.window.InputEvent('input', options)) }
  try { run({ node, sent, keyboard, event, input, window: dom.window }) }
  finally { keyboard.destroy(); dom.window.close() }
}

test('phone input, composition, paste and deletion send RFB keys once', () => setup(({ node, sent, event, input, window }) => {
  input(node.value + 'Hi')
  event('compositionstart')
  input(node.value + 'に', { isComposing: true })
  assert.deepEqual(sent, [[72], [105]])
  node.value += '本'
  node.dispatchEvent(new window.CompositionEvent('compositionend', { data: '日本' }))
  event('input') // Some keyboards send a final input after compositionend.
  const paste = Object.assign(new window.Event('paste', { cancelable: true }), { clipboardData: { getData: () => 'a\nb😀' } })
  node.dispatchEvent(paste)
  input('', { inputType: 'deleteContentBackward' })
  assert.deepEqual(sent, [[72], [105], [0x10065e5], [0x100672c], [97], [0xff0d], [98], [0x101f600], [0xff08]])
  assert.equal(paste.defaultPrevented, true)
}))

test('Ctrl and Alt apply to one key and release before the next key', () => setup(({ keyboard, sent, node, input }) => {
  keyboard.key('ctrl')
  keyboard.key('alt')
  input(node.value + 'lx')
  keyboard.key('tab')
  assert.deepEqual(sent, [
    [0xffe3, 'ControlLeft', true], [0xffe9, 'AltLeft', true], [108],
    [0xffe9, 'AltLeft', false], [0xffe3, 'ControlLeft', false], [120], [0xff09],
  ])
}))

test('hardware special keys prevent native edits and do not duplicate input', () => setup(({ node, sent, window }) => {
  const e = new window.KeyboardEvent('keydown', { key: 'Enter', cancelable: true })
  node.dispatchEvent(e)
  assert.equal(e.defaultPrevented, true)
  assert.deepEqual(sent, [[0xff0d]])
}))

test('cancelled composition sends no unfinished text', () => setup(({ node, sent, event, input, window }) => {
  event('compositionstart')
  input(node.value + 'draft', { isComposing: true })
  node.dispatchEvent(new window.CompositionEvent('compositionend', { data: '' }))
  event('input')
  assert.deepEqual(sent, [])
}))

test('a new control session publishes cleared modifiers', () => {
  const dom = new JSDOM('<textarea></textarea>')
  const node = textarea(dom)
  let displayed = { ctrl: false, alt: false }
  const options = { sendKey: () => {}, changed: (value: { ctrl: boolean, alt: boolean }) => (displayed = value) }
  const first = phoneKeyboard(node, options)
  first.key('ctrl')
  first.key('alt')
  assert.deepEqual(displayed, { ctrl: true, alt: true })
  first.destroy()
  const second = phoneKeyboard(node, options)
  try { assert.deepEqual(displayed, { ctrl: false, alt: false }) }
  finally { second.destroy(); dom.window.close() }
})
