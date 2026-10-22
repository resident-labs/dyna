import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { sourceRange, stripTypes } from '../../../test/svelte-source.ts'

const source = readFileSync(new URL('./Pair.svelte', import.meta.url), 'utf8')
const script = sourceRange(parse(source, { modern: true }).instance?.content ?? {})
const code = stripTypes(source.slice(script.start, script.end).replace(/^\s*import .*$/gm, ''))

type PairCall = (code: string, name: string) => Promise<void>

function pairing(hash: string, pair: PairCall) {
  let mount: () => void = () => assert.fail('the screen did not register onMount')
  let reloads = 0
  const location = { hash, pathname: '/', search: '', reload() { reloads++ } }
  const context = vm.createContext({
    location, URLSearchParams,
    history: { replaceState() { location.hash = '' } },
    navigator: { userAgent: 'WebKit wails.io' },
    $state: <T>(value: T) => value,
    onMount: (fn: () => void) => { mount = fn },
    pair, connect: null,
  })
  vm.runInContext(code, context)
  return { context, mount: () => mount(), location, reloads: () => reloads }
}

test('a pairing link submits once on mount and clears the code from the URL', async () => {
  const calls: [string, string][] = []
  const screen = pairing('#pair=ABCDE-FGHJK', async (code, name) => { calls.push([code, name]) })
  screen.mount()
  await new Promise((resolve) => setImmediate(resolve))
  assert.deepEqual(calls, [['ABCDE-FGHJK', 'Dyna desktop']])
  assert.equal(screen.location.hash, '')
  assert.equal(screen.reloads(), 1)
})

test('manual and invalid codes do not submit on mount', () => {
  for (const hash of ['', '#pair=%3Cscript%3E']) {
    const screen = pairing(hash, () => assert.fail('unexpected pairing call'))
    screen.mount()
    assert.equal(screen.reloads(), 0)
  }
})

test('a failed automatic pairing keeps the error for a manual retry', async () => {
  const screen = pairing('#pair=ABCDE-FGHJK', async () => { throw new Error('code expired') })
  screen.mount()
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(screen.reloads(), 0)
  assert.match(String(vm.runInContext('error', screen.context)), /code expired/)
  assert.equal(vm.runInContext('busy', screen.context), false)
})
