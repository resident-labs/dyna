import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { build } from 'esbuild'
import { compileSvelte, svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { importBundle, installDom, domNames } from '../prs/prDetailHarness.ts'

test('filtering out the selection prevents starting it', async () => {
  const source = `<script>import RunWorkflow from './RunWorkflow.svelte'</script>
    <RunWorkflow room={{id:'room', name:'room'}} onstarted={() => {}} oncancel={() => {}} />`
  const result = await build({ bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'], logLevel: 'silent',
    stdin: { contents: compileSvelte(source, 'PickerFixture.svelte') + '\nexport { mount, unmount, flushSync } from "svelte"', resolveDir: new URL('.', import.meta.url).pathname },
    plugins: [svelteEsbuildPlugin(), { name: 'picker-api', setup(b) { b.onLoad({ filter: /[/\\]api\.ts$/ }, () => ({ contents: `export const api = { listWorkflows: async () => [{name:'audit',scope:'bundled',description:'Audit'}], startNamedRun: async () => { throw Error('unexpected start') } }`, loader: 'js' })) } }],
  })
  const output = result.outputFiles?.[0]
  assert.ok(output)
  const dom = installDom()
  const ui = await importBundle(output.contents, 'PickerFixture')
  const app = ui.mount(ui.default, { target: document.getElementById('app') })
  try {
    ui.flushSync(); await Promise.resolve(); await Promise.resolve(); ui.flushSync()
    assert.equal(document.querySelector<HTMLButtonElement>('button.go')?.disabled, false)
    const search = document.querySelector<HTMLInputElement>('input.search')
    assert.ok(search)
    search.value = 'missing'
    search.dispatchEvent(new dom.window.Event('input', { bubbles: true }))
    ui.flushSync()
    assert.equal(document.querySelector<HTMLButtonElement>('button.go')?.disabled, true)
  } finally {
    await ui.unmount(app); dom.window.close()
    for (const name of domNames) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})
