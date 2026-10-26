import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { build } from 'esbuild'
import { compileSvelte, svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { importBundle, installDom, domNames } from '../prs/prDetailHarness.ts'

const fixture = (phases: [string, string]) => `<script>
 import Run from './Run.svelte'
 const agent = (id, phase) => ({ id, label: 'T' + id, phase, status: 'ok' })
 const make = (run) => ({ run: { id: run, name: 'continuous', status: 'running', startedAt: new Date().toISOString() },
  agents: [agent(1, '${phases[0]}'), agent(2, '${phases[1]}')], agentTotal: 2, agentRunning: 0, agentFailed: 0, logs: [], phases: [] })
 let detail = $state(make('wf_a'))
 export function refresh(run) { detail = make(run) }
</script>
<Run {detail} />`
const bundle = async (source: string) => {
  const compiled = await build({ bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'], logLevel: 'silent',
    stdin: { contents: compileSvelte(source, 'RunFixture.svelte') + '\nexport { mount, unmount, flushSync } from "svelte"', resolveDir: new URL('.', import.meta.url).pathname },
    plugins: [svelteEsbuildPlugin(), { name: 'run-api', setup(b) { b.onLoad({ filter: /[/\\]api\.ts$/ }, () => ({ contents: 'export const api = {}; export const copyText = async () => {}', loader: 'js' })) } }],
  })
  const output = compiled.outputFiles?.[0]
  assert.ok(output, 'the run fixture built')
  return output
}
const output = await bundle(fixture(['CH prepare', 'BS prepare']))
const plain = await bundle(fixture(['Plan', 'Build']))

test('an open cell stays open when the same run refreshes, and closes for another run', async () => {
  const dom = installDom()
  const ui = await importBundle(output.contents, 'RunFixture')
  const app = ui.mount(ui.default, { target: document.getElementById('app') })
  const rows = () => document.querySelectorAll('.agent').length
  try {
    ui.flushSync()
    assert.equal(rows(), 0, 'no cell is open at first')
    document.querySelector<HTMLElement>('button.cell')?.click()
    ui.flushSync()
    assert.equal(rows(), 1)
    ui.flushSync(() => app.refresh('wf_a'))
    assert.equal(rows(), 1, 'a refresh of the same run keeps the cell open')
    ui.flushSync(() => app.refresh('wf_b'))
    assert.equal(rows(), 0, 'another run starts with no cell open')
  } finally {
    await ui.unmount(app); dom.window.close()
    for (const name of domNames) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})

test('a phase row opens its agents and folds them again', async () => {
  const dom = installDom()
  const ui = await importBundle(plain.contents, 'RunFixture')
  const app = ui.mount(ui.default, { target: document.getElementById('app') })
  const rows = () => document.querySelectorAll('.agent').length
  try {
    ui.flushSync()
    assert.equal(rows(), 0, 'agents start folded')
    document.querySelector<HTMLElement>('.phases button.row')?.click()
    ui.flushSync()
    assert.equal(rows(), 1)
    document.querySelector<HTMLElement>('.phases button.row')?.click()
    ui.flushSync()
    assert.equal(rows(), 0)
  } finally {
    await ui.unmount(app); dom.window.close()
    for (const name of domNames) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})
