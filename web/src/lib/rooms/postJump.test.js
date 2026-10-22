import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'

// Run Feed's real jump() and its helpers against a fake list.
const source = readFileSync(new URL('./Feed.svelte', import.meta.url), 'utf8')
const body = parse(source).instance.content.body
const named = (name) => body.find((n) => (n.declaration ?? n).id?.name === name || n.declarations?.[0]?.id.name === name)
const code = ['rowOf', 'jump'].map((name) => {
  const n = named(name)
  return source.slice((n.declaration ?? n).start, (n.declaration ?? n).end)
}).join('\n')

const row = (id, posts) => ({ kind: 'post', key: id, post: { id }, ...(posts ? { posts: posts.map((p) => ({ id: p })) } : {}) })

function feed({ rows, earlier = 0, onaround, onreveal }) {
  const calls = []
  const c = {
    rows, earlier, onaround, onreveal, calls, stuck: true, flash: null, jumps: 0, timers: [],
    list: { scrollToIndex(i) { calls.push(`scroll ${i}`) }, scrollToEnd() { calls.push('end') } },
    tick: async () => {},
    setTimeout(f) { c.timers.push(f) },
  }
  vm.createContext(c)
  vm.runInContext(code, c)
  return c
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
  c.timers[0]()
  assert.equal(c.flash.key, 'b')
  c.timers[1]()
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
 c.onaround=async id=>{
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
  c.onreveal = (id) => { c.rows = [...c.rows, row(id)] }
  await c.jump('hidden')
  assert.deepEqual(c.calls, ['scroll 1'])
})
