import test from 'node:test'
import assert from 'node:assert/strict'
import { call, runFunctions, topLevelFunctions } from '../test/svelte-source.ts'

const typed = `<script lang="ts">
  interface Entry { name: string, count: number }
  let entries: Entry[] = $state([])
  let total = 0
  function add(name: string, count: number = 1): Entry {
    const entry: Entry = { name, count }
    entries.push(entry)
    total += count
    return entry
  }
  async function later<T>(value: T): Promise<T> { return value }
</script>
<button onclick={() => add('x')}>add</button>
`

test('typed top-level functions run in a vm context', async () => {
  const context: Record<string, unknown> = { entries: [], total: 0 }
  runFunctions(typed, ['add', 'later'], context)
  assert.equal(JSON.stringify(call(context, 'add', 'a', 2)), '{"name":"a","count":2}')
  assert.equal(context.total, 2)
  assert.equal(await call(context, 'later', 'v'), 'v')
})

test('function offsets stay correct around a typed script', () => {
  assert.deepEqual([...topLevelFunctions(typed).keys()], ['add', 'later'])
})

test('plain JavaScript functions run unchanged', () => {
  const context: Record<string, unknown> = {}
  runFunctions('<script>\n  function double(n) { return n * 2 }\n</script>', ['double'], context)
  assert.equal(call(context, 'double', 4), 8)
})

test('a missing function name is an error', () => {
  assert.throws(() => runFunctions(typed, ['nope'], {}), /no top-level function nope/)
})

test('only requested functions are extracted, then run in request order', () => {
  const source = `<script lang="ts">
    let seen: string[] = []
    function keep(label: string): void { seen.push(label) }
    function skip(label: string): void { seen.push('skip:' + label) }
    function also(label: string): void { seen.push(label) }
  </script>`
  assert.deepEqual([...topLevelFunctions(source, ['also', 'keep']).keys()], ['keep', 'also'])
  const seen: string[] = []
  const context: Record<string, unknown> = { seen }
  runFunctions(source, ['also', 'keep'], context)
  call(context, 'also', 'a')
  call(context, 'keep', 'b')
  assert.deepEqual(seen, ['a', 'b'])
  assert.equal(context.skip, undefined)
})
