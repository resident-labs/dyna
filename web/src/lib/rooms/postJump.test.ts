import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { sourceRange, stripTypes } from '../../../test/svelte-source.ts'

// Run Feed's real jump() and its helpers against a fake list.
const source = readFileSync(new URL('./Feed.svelte', import.meta.url), 'utf8')
const body = parse(source, { modern: true }).instance?.content.body ?? []
type ScriptNode = (typeof body)[number]
// An exported function is declared inside its export statement.
const named = (name: string): ScriptNode | undefined => {
  for (const entry of body) {
    const node = entry.type === 'ExportNamedDeclaration' ? entry.declaration : entry
    if (!node) continue
    if (node.type === 'FunctionDeclaration' && node.id?.name === name) return node
    if (node.type === 'VariableDeclaration' && node.declarations[0]?.id.type === 'Identifier' && node.declarations[0].id.name === name) return node
  }
  return undefined
}
const code = ['rowOf', 'jump'].map((name) => {
  const n = named(name)
  assert.ok(n, name)
  const { start, end } = sourceRange(n)
  return source.slice(start, end)
}).join('\n')

interface Row {
  kind: string
  key: string
  post: { id: string }
  posts?: { id: string }[]
}

interface Flash {
  key?: string
  id?: string
  n?: number
}

const row = (id: string, posts?: string[]): Row => ({ kind: 'post', key: id, post: { id }, ...(posts ? { posts: posts.map((p) => ({ id: p })) } : {}) })

interface FeedOptions {
  rows: Row[]
  earlier?: number
  onaround?: (id: string) => Promise<boolean>
  onreveal?: (id: string) => void
}

interface FeedState {
  rows: Row[]
  earlier: number
  onaround?: (id: string) => Promise<boolean>
  onreveal?: (id: string) => void
  calls: string[]
  stuck: boolean
  flash: Flash | null
  jumps: number
  timers: (() => void)[]
  list: { scrollToIndex(index: number): void; scrollToEnd(): void }
  tick: () => Promise<void>
  setTimeout(fn: () => void): void
}

interface FeedApp extends FeedState {
  jump: (id: string) => Promise<void>
}

function feed({ rows, earlier = 0, onaround, onreveal }: FeedOptions) {
  const calls: string[] = []
  const c: FeedState = {
    rows, earlier, onaround, onreveal, calls, stuck: true, flash: null, jumps: 0, timers: [],
    list: { scrollToIndex(i: number) { calls.push(`scroll ${i}`) }, scrollToEnd() { calls.push('end') } },
    tick: async () => {},
    setTimeout(f: () => void) { c.timers.push(f) },
  }
  vm.createContext(c)
  vm.runInContext(stripTypes(code), c)
  const fns = vm.runInContext('({ jump, rowOf })', c) as Pick<FeedApp, 'jump'>
  return Object.assign(c, fns)
}

test('a jump scrolls the list to the post and marks its row to flash', async () => {
  const c = feed({ rows: [row('a'), row('b'), row('c')] })
  await c.jump('b')
  assert.deepEqual(c.calls, ['scroll 1'])
  assert.deepEqual({ ...c.flash }, { key: 'b', id: 'b', n: 1 })
  assert.equal(c.stuck, false)
})

test('the flash clears after its time, but not a newer jump\'s flash', async () => {
  const c = feed({ rows: [row('a'), row('b')] })
  // Copy the state as a Svelte proxy would: same values, new identity.
  const wrap = () => { c.flash = { ...c.flash } }
  await c.jump('a'); wrap()
  await c.jump('b'); wrap()
  c.timers[0]?.()
  assert.equal(c.flash?.key, 'b')
  c.timers[1]?.()
  assert.equal(c.flash, null)
})

test('a jump to a post folded into a PR row finds that row and names the notice', async () => {
  const c = feed({ rows: [row('a'), row('p3', ['p1', 'p2', 'p3'])] })
  await c.jump('p1')
  assert.deepEqual(c.calls, ['scroll 1'])
  assert.deepEqual({ ...c.flash }, { key: 'p3', id: 'p1', n: 1 })
})

test('a jump loads one direct page around an unloaded target', async () => {
 let loads=0
 const c=feed({rows:[row('new')],earlier:10000})
 c.onaround=async (id: string)=>{
  loads++
  assert.equal(c.stuck,false)
  assert.equal(id,'old')
  c.rows=[row('old')]
  return true
 }
 await c.jump('old')
 assert.equal(loads,1)
 assert.deepEqual(c.calls,['scroll 0'])
})

test('a jump stops when its direct page fails', async () => {
 let loads=0
 const c=feed({rows:[row('new')],onaround:async()=>{loads++;return false}})
 await c.jump('gone')
 assert.equal(loads,1)
 assert.deepEqual(c.calls,[])
})

test('a jump asks the app to reveal a post hidden from the feed', async () => {
  const c = feed({ rows: [row('a')] })
  c.onreveal = (id: string) => { c.rows = [...c.rows, row(id)] }
  await c.jump('hidden')
  assert.deepEqual(c.calls, ['scroll 1'])
})
