import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { copyText } from '../../api.js'
import { postHref } from '../ui/posts.js'

const source = readFileSync(new URL('../ui/PostActions.svelte', import.meta.url), 'utf8')
const handlers = new Map()
function walk(node) {
  if (!node || typeof node !== 'object') return
  if (Array.isArray(node)) { node.forEach(walk); return }
  if (node.type === 'RegularElement' && node.name === 'button') {
    const title = node.attributes.find((attr) => attr.name === 'title')?.value?.[0]?.data
    if (['Copy text', 'Copy link'].includes(title)) {
      const value = node.attributes.find((attr) => attr.name === 'onclick').value
      const handler = (Array.isArray(value) ? value[0] : value).expression
      handlers.set(title, source.slice(handler.start, handler.end))
    }
  }
  for (const [key, child] of Object.entries(node)) if (key !== 'attributes') walk(child)
}
walk(parse(source, { modern: true }).fragment)
assert.equal(handlers.size, 2)

function globals(t, clipboard) {
  const previous = new Map()
  for (const [key, value] of Object.entries({ navigator: { clipboard }, window: {} })) {
    previous.set(key, Object.getOwnPropertyDescriptor(globalThis, key))
    Object.defineProperty(globalThis, key, { configurable: true, value, writable: true })
  }
  t.after(() => {
    for (const [key, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor)
      else delete globalThis[key]
    }
  })
}
async function copyActions() {
  const room = { id: 'room_20261017-123456-abcdef12' }
  const p = { id: 'msg_report', text: 'Full report text' }
  const c = vm.createContext({ done() {}, copyText, postHref, URL, navigator, room, p, location: { href: 'http://wails.localhost/' } })
  for (const code of handlers.values()) await vm.runInContext(`(${code})()`, c)
  return [p.text, 'http://wails.localhost/' + postHref(room.id, p.id)]
}

test('post text and link actions use the browser clipboard when it succeeds', async (t) => {
  const copied = []
  globals(t, { async writeText(text) { copied.push(text) } })
  const expected = await copyActions()
  assert.deepEqual(copied, expected)
})

test('clipboard failures reach the caller', async (t) => {
  globals(t, { async writeText() { throw new Error('browser clipboard unavailable') } })
  await assert.rejects(copyActions(), /browser clipboard unavailable/)
})
