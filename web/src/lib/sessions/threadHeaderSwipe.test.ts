import test from 'node:test'
import assert from 'node:assert/strict'
import { JSDOM } from 'jsdom'
import { threadHeaderSwipe } from './threadHeaderSwipe.ts'

test('thread headers accept only clear single-touch right swipes outside controls and selections', () => {
  const dom = new JSDOM('<header><span>Thread title</span><button>Close</button></header><p>Other content</p>')
  const doc = dom.window.document
  const header = doc.querySelector('header')
  const title = doc.querySelector('span')
  const button = doc.querySelector('button')
  const outside = doc.querySelector('p')
  assert.ok(header && title && button && outside)
  let backs = 0
  const action = threadHeaderSwipe(header, { enabled: true, back: () => backs++ })
  const send = (target: EventTarget, type: string, x: number, y: number, id = 1, pointerType = 'touch') => {
    target.dispatchEvent(Object.assign(new dom.window.Event(type, { bubbles: true }), { pointerId: id, pointerType, isPrimary: id === 1, clientX: x, clientY: y }))
  }
  const swipe = (target: EventTarget, x: number, y: number, pointerType = 'touch') => {
    send(target, 'pointerdown', 0, 0, 1, pointerType)
    send(doc, 'pointerup', x, y, 1, pointerType)
  }
  try {
    swipe(title, 80, 10)
    assert.equal(backs, 1)
    swipe(title, 30, 0); swipe(title, -90, 0); swipe(title, 90, 60); swipe(title, 80, 0, 'mouse')
    swipe(button, 90, 0); swipe(outside, 90, 0)
    send(title, 'pointerdown', 0, 0); send(outside, 'pointerdown', 10, 0, 2)
    send(doc, 'pointerup', 90, 0); send(doc, 'pointerup', 90, 0, 2)
    send(title, 'pointerdown', 0, 0); send(doc, 'pointermove', 5, 50); send(doc, 'pointerup', 90, 10)
    const selection = doc.getSelection()
    const range = doc.createRange(); range.selectNodeContents(title); selection?.addRange(range)
    swipe(title, 90, 0); selection?.removeAllRanges()
    assert.equal(backs, 1)
    action.update({ enabled: false, back: () => backs++ }); swipe(title, 90, 0)
    assert.equal(backs, 1)
  } finally { action.destroy(); dom.window.close() }
})
