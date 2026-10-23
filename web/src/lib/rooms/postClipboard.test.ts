import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import ts from 'typescript'
import { copyText } from '../../api.ts'
import { postHref } from '../ui/posts.ts'

const source = readFileSync(new URL('../ui/PostActions.svelte', import.meta.url), 'utf8')
const handlers = new Map<string, string>()
// The template walk reads only node names, attributes and source ranges.
type Node = Record<string, unknown>
const asNode = (value: unknown): Node | undefined => (value !== null && typeof value === 'object' ? value as Node : undefined)
function range(node: unknown): { start: number; end: number } {
  if (typeof node !== 'object' || node === null || typeof Reflect.get(node, 'start') !== 'number' || typeof Reflect.get(node, 'end') !== 'number') throw new Error('the copy handler has no source range')
  return { start: Reflect.get(node, 'start') as number, end: Reflect.get(node, 'end') as number }
}
function walk(node: unknown): void {
  if (!node || typeof node !== 'object') return
  if (Array.isArray(node)) { node.forEach(walk); return }
  const item = node as Node
  const attributes = Array.isArray(item.attributes) ? item.attributes.map(asNode).filter((a) => a !== undefined) : []
  if (item.type === 'RegularElement' && item.name === 'button') {
    const titleValue = attributes.find((attr) => attr.name === 'title')?.value
    const title = Array.isArray(titleValue) ? asNode(titleValue[0])?.data : undefined
    if (title === 'Copy text' || title === 'Copy link') {
      const value = attributes.find((attr) => attr.name === 'onclick')?.value
      const handler = asNode(Array.isArray(value) ? value[0] : value)?.expression
      const { start, end } = range(handler)
      handlers.set(title, source.slice(start, end))
    }
  }
  for (const [key, child] of Object.entries(item)) if (key !== 'attributes') walk(child)
}
walk(parse(source, { modern: true }).fragment)
assert.equal(handlers.size, 2)

interface Clipboard {
  writeText(text: string): Promise<void>
}

function globals(t: { after(fn: () => void): void }, clipboard: Clipboard) {
  const previous = new Map<string, PropertyDescriptor | undefined>()
  for (const [key, value] of Object.entries({ navigator: { clipboard }, window: {} })) {
    previous.set(key, Object.getOwnPropertyDescriptor(globalThis, key))
    Object.defineProperty(globalThis, key, { configurable: true, value, writable: true })
  }
  t.after(() => {
    for (const [key, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor)
      else Reflect.deleteProperty(globalThis, key)
    }
  })
}
const script = parse(source, { modern: true }).instance
assert.ok(script, 'PostActions has an instance script')
const copyNode = script.content.body.find((node) => node.type === 'FunctionDeclaration' && node.id?.name === 'copy')
assert.ok(copyNode, 'PostActions has a copy function')
const copySource = ts.transpile(source.slice(range(copyNode).start, range(copyNode).end), { target: ts.ScriptTarget.ES2022 })

test('post copy actions report success, catch failure, and ignore stale results', async (t) => {
  const written: string[] = []
  let reject = false
  // The clipboard write parks until the test releases it; a holder keeps the late assignment visible.
  const parked: { release: (() => void) | null | undefined } = { release: undefined }
  globals(t, { async writeText(text: string) {
    if (reject) throw new Error('browser clipboard unavailable')
    written.push(text)
    if (parked.release !== undefined) await new Promise<void>((resolve) => { parked.release = () => resolve() })
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
  Object.assign(parked, { release: null })
  const pending = vm.runInContext('copy("text")', c)
  c.generation++
  parked.release?.()
  await pending
  assert.equal(c.copied, '')
  assert.equal(c.copyError, false)
})
