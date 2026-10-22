import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { parse } from 'svelte/compiler'
import vm from 'node:vm'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const ast = parse(source, { modern: true })
assert.ok(ast.instance)
const node = ast.instance.content.body.find((node) => node.type === 'FunctionDeclaration' && node.id?.name === 'loadSync')
assert.ok(node && 'start' in node && typeof node.start === 'number' && 'end' in node && typeof node.end === 'number')
const loadSource = source.slice(node.start, node.end)

type Counts = Record<string, { origin: { ahead: number; behind: number } }>
function loader(syncStatus: () => Promise<Counts>) {
  const state = {
    syncStatus: {} as Counts,
    loadError: '',
    api: { syncStatus },
    reload: (_name: string, run: (current: () => boolean) => Promise<void>) => run(() => true),
  }
  const context = vm.createContext(state)
  vm.runInContext(loadSource, context)
  return { state, load: (): Promise<void> => vm.runInContext('loadSync()', context) }
}

test('a failed background sync status read shows no error banner', async () => {
  const app = loader(async () => { throw new Error('this device cannot use SyncStatus') })
  await app.load()
  assert.equal(app.state.loadError, '', 'a background status call must not raise the room banner')
  assert.deepEqual(app.state.syncStatus, {})
})

test('a successful sync status read still updates the saved counts', async () => {
  const app = loader(async () => ({ '/r': { origin: { ahead: 0, behind: 2 } } }))
  await app.load()
  assert.equal(app.state.loadError, '')
  assert.equal(app.state.syncStatus['/r']?.origin.behind, 2)
})
