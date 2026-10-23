import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import ts from 'typescript'
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
const script = parse(source, { modern: true }).instance
const copyNode = script.content.body.find((node) => node.type === 'FunctionDeclaration' && node.id.name === 'copy')
const copySource = ts.transpile(source.slice(copyNode.start, copyNode.end), { target: ts.ScriptTarget.ES2022 })

test('post copy actions report success, catch failure, and ignore stale results', async (t) => {
  const written = []
  let reject = false
  let release
  globals(t, { async writeText(text) {
    if (reject) throw new Error('browser clipboard unavailable')
    written.push(text)
    if (release !== undefined) await new Promise((resolve) => { release = resolve })
  } })
  const room = { id: 'room_20261017-123456-abcdef12' }
  const p = { id: 'msg_report', text: 'Full report text' }
  const c = vm.createContext({
    copyText, postHref, URL, room, p, location: { href: 'http://wails.localhost/' },
    generation: 0, copied: '', copyError: false, timer: undefined,
    clearTimeout() {}, setTimeout() { return 1 },
  })
  vm.runInContext(copySource, c)
  for (const [title, code] of handlers) {
    await vm.runInContext(`(${code})()`, c)
    assert.equal(c.copied, title === 'Copy text' ? 'text' : 'link')
    assert.equal(c.copyError, false)
  }
  assert.deepEqual(written, [p.text, 'http://wails.localhost/' + postHref(room.id, p.id)])
  reject = true
  for (const code of handlers.values()) {
    await vm.runInContext(`(${code})()`, c)
    assert.equal(c.copied, '')
    assert.equal(c.copyError, true)
  }
  reject = false
  release = null
  const pending = vm.runInContext('copy("text")', c)
  c.generation++
  release()
  await pending
  assert.equal(c.copied, '')
  assert.equal(c.copyError, false)
})
