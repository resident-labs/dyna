import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { resolve } from 'node:path'
import { build } from 'esbuild'
import { compileSvelte, svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { importBundle, installDom, domNames } from '../prs/prDetailHarness.ts'

const source = `<script>
 import RulesPane from './RulesPane.svelte'
 let version = $state(0)
 export function changed() { version++ }
</script>
<RulesPane {version} />`
const compiled = await build({ bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'], logLevel: 'silent',
 stdin: { contents: compileSvelte(source, 'RulesPaneFixture.svelte') + '\nexport { mount, unmount, flushSync } from "svelte"', resolveDir: new URL('.', import.meta.url).pathname },
 // api.ts keeps its other exports; only api is replaced.
 plugins: [svelteEsbuildPlugin(), { name: 'rules-api', setup(b) {
  b.onResolve({ filter: /^actual-api:/ }, ({ path }) => ({ path: path.slice('actual-api:'.length), namespace: 'actual-api' }))
  b.onLoad({ filter: /.*/, namespace: 'actual-api' }, ({ path }) => ({ contents: readFileSync(path, 'utf8').replace(/^export const api\b/m, 'const actualApi'), loader: 'ts', resolveDir: resolve(path, '..') }))
  b.onLoad({ filter: /[/\\]api\.ts$/, namespace: 'file' }, ({ path }) => ({ contents: `export * from ${JSON.stringify('actual-api:' + path)};\nexport const api = globalThis.rulesApi`, loader: 'ts', resolveDir: resolve(path, '..') }))
 } }],
})
const output = compiled.outputFiles?.[0]
assert.ok(output, 'the rules pane fixture built')

test('the Settings Rules pane shows global rules another client changed, but not during an edit', async () => {
 const dom = installDom(); globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
 let text = 'First text'
 Reflect.set(globalThis, 'rulesApi', { rules: async () => ({ hash: text, used: 0, max: 3000, rules: [{ id: 'u1', text, setter: 'owner' }] }) })
 const ui = await importBundle(output.contents, 'RulesPaneFixture')
 const app = ui.mount(ui.default, { target: document.getElementById('app') })
 const settle = async () => { for (let i = 0; i < 4; i++) { await Promise.resolve(); ui.flushSync() } }
 const body = () => document.body.textContent ?? ''
 try {
  await settle(); assert.match(body(), /First text/)
  text = 'Second text'; ui.flushSync(() => app.changed()); await settle()
  assert.match(body(), /Second text/)
  document.getElementById('rule-control-u1')?.click(); await settle()
  text = 'Third text'; ui.flushSync(() => app.changed()); await settle()
  const field = document.getElementById('rule-edit-u1') as HTMLTextAreaElement | null
  assert.equal(field?.value, 'Second text')
  field?.dispatchEvent(new dom.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); await settle()
  assert.match(body(), /Third text/)
 } finally {
  await ui.unmount(app); dom.window.close()
  for (const name of [...domNames, 'HTMLMediaElement', 'rulesApi']) Reflect.deleteProperty(globalThis, name)
  await rm(ui.dir, { recursive: true, force: true })
 }
})
