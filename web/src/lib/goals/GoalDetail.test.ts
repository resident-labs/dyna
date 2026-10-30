import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { build } from 'esbuild'
import { compileSvelte, svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { importBundle, installDom, domNames } from '../prs/prDetailHarness.ts'
const source = `<script>
 import GoalDetail from './GoalDetail.svelte'
 let goal = $state({ id: 'a', text: 'Goal', state: 'active', shared: true })
 export function select(id) { goal = { ...goal, id } }
</script>
<GoalDetail roomId="global" {goal} onchanged={() => {}} onclose={() => {}} />`
const compiled = await build({ bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'], logLevel: 'silent',
 stdin: { contents: compileSvelte(source, 'GoalFixture.svelte') + '\nexport { mount, unmount, flushSync } from "svelte"', resolveDir: new URL('.', import.meta.url).pathname },
 plugins: [svelteEsbuildPlugin(), { name: 'goal-api', setup(b) { b.onLoad({ filter: /[/\\]api\.ts$/ }, () => ({ contents: 'export const api = globalThis.goalApi', loader: 'js' })) } }],
})
const output = compiled.outputFiles?.[0]
assert.ok(output, 'the goal fixture built')
test('goal reads distinguish pending, failure and empty while discarding stale selected-goal replies', async () => {
 const dom = installDom(); globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
 interface PendingRead { id: string; resolve: (value: unknown) => void; reject: (reason: unknown) => void }
 const pending: { status: PendingRead[]; items: PendingRead[] } = { status: [], items: [] }
 const read = (kind: 'status' | 'items') => (_room: string, id: string) => new Promise<unknown>((resolve, reject) => pending[kind].push({ id, resolve, reject }))
 Reflect.set(globalThis, 'goalApi', { goalStatus: read('status'), goalItems: read('items') })
 const ui = await importBundle(output.contents, 'GoalFixture')
 const app = ui.mount(ui.default, { target: document.getElementById('app') })
 const settle = async () => { await Promise.resolve(); ui.flushSync() }
 const text = () => document.body.textContent ?? ''
 const clickRetry = () => document.querySelector<HTMLElement>('.read-error button')?.click()
 try {
  ui.flushSync(); assert.equal(document.querySelector('h2')?.textContent, 'Goal'); assert.match(text(), /Loading checklist/); assert.doesNotMatch(text(), /No items\./)
  ui.flushSync(() => app.select('b')); ui.flushSync(() => app.select('a'))
  pending.items[0]?.resolve([{ id: 'old', text: 'Old result' }]); pending.status[1]?.resolve({ label: 'stuck', reason: 'Wrong goal' }); await settle()
  assert.doesNotMatch(text(), /Old result|Wrong goal/)
  pending.items[2]?.resolve([{ id: 'new', text: 'Current item' }]); pending.status[2]?.reject(Error('offline')); await settle()
  assert.match(text(), /Current item/); assert.match(text(), /Status unavailable: offline/)
  clickRetry(); ui.flushSync()
  assert.equal(pending.items.length, 3); pending.status[3]?.resolve({ label: 'on_track' }); await settle()
  assert.match(text(), /On track/); assert.match(text(), /Current item/)
  ui.flushSync(() => app.select('c')); pending.status[4]?.resolve({ label: 'waiting' }); pending.items[3]?.reject(Error('read failed')); await settle()
  assert.match(text(), /Waiting/); assert.match(text(), /Checklist unavailable: read failed/); assert.doesNotMatch(text(), /No items\./)
  clickRetry(); ui.flushSync(); assert.equal(pending.status.length, 5)
  pending.items[4]?.resolve([]); await settle(); assert.match(text(), /No items\./)
 } finally {
  await ui.unmount(app); dom.window.close()
  for (const name of [...domNames, 'HTMLMediaElement', 'goalApi']) Reflect.deleteProperty(globalThis, name)
  await rm(ui.dir, { recursive: true, force: true })
 }
})
