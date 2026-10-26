import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { build } from 'esbuild'
import { compileSvelte, svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { importBundle, installDom, domNames } from '../prs/prDetailHarness.ts'

const fixture = (phases: [string, string, string]) => `<script>
 import Run from './Run.svelte'
 const agent = (id, phase) => ({ id, label: 'T' + id, phase, status: 'ok', session: 'ses_' + id })
 const make = (run) => ({ run: { id: run, name: 'continuous', status: 'running', startedAt: new Date().toISOString() },
  agents: [agent(1, '${phases[0]}'), agent(4, '${phases[0]}'), agent(2, '${phases[1]}'), agent(3, '${phases[2]}')], agentTotal: 4, agentRunning: 0, agentFailed: 0, logs: [], phases: [] })
 let detail = $state(make('wf_a'))
 export function refresh(run) { detail = make(run) }
 export function dropFirst() { detail = { ...detail, agents: detail.agents.slice(2) } }
 export function addAgent(id, phase) { detail = { ...detail, agents: [...detail.agents.slice(0, 2), agent(id, phase), ...detail.agents.slice(2)] } }
 export function dropAgent(id) { detail = { ...detail, agents: detail.agents.filter((a) => a.id !== id) } }
</script>
<Run {detail} onopen={() => {}} />`
const bundle = async (source: string) => {
  const compiled = await build({ bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'], logLevel: 'silent',
    stdin: { contents: compileSvelte(source, 'RunFixture.svelte') + '\nexport { mount, unmount, flushSync } from "svelte"', resolveDir: new URL('.', import.meta.url).pathname },
    plugins: [svelteEsbuildPlugin(), { name: 'run-api', setup(b) { b.onLoad({ filter: /[/\\]api\.ts$/ }, () => ({ contents: 'export const api = {}; export const copyText = async () => {}', loader: 'js' })) } }],
  })
  const output = compiled.outputFiles?.[0]
  assert.ok(output, 'the run fixture built')
  return output
}
const output = await bundle(fixture(['CH prepare', 'BS prepare', 'CH review']))
const plain = await bundle(fixture(['Plan', 'Build', 'Ship']))

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
    assert.equal(rows(), 2)
    ui.flushSync(() => app.refresh('wf_a'))
    assert.equal(rows(), 2, 'a refresh of the same run keeps the cell open')
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
    assert.equal(rows(), 2)
    document.querySelector<HTMLElement>('.phases button.row')?.click()
    ui.flushSync()
    assert.equal(rows(), 0)
  } finally {
    await ui.unmount(app); dom.window.close()
    for (const name of domNames) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})

test('an open phase row stays on its own group when an earlier group leaves the agent window', async () => {
  const dom = installDom()
  const ui = await importBundle(plain.contents, 'RunFixture')
  const app = ui.mount(ui.default, { target: document.getElementById('app') })
  const labels = () => [...document.querySelectorAll('.agent .label')].map((n) => n.textContent)
  try {
    ui.flushSync()
    document.querySelectorAll<HTMLElement>('.phases button.row')[1]?.click()
    ui.flushSync()
    assert.deepEqual(labels(), ['T2'])
    ui.flushSync(() => app.dropFirst())
    assert.deepEqual(labels(), ['T2'], 'the Build row stays open after the Plan group leaves')
  } finally {
    await ui.unmount(app); dom.window.close()
    for (const name of domNames) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})

test('an open phase row stays open when the window trims only some of its agents', async () => {
  const dom = installDom()
  const ui = await importBundle(plain.contents, 'RunFixture')
  const app = ui.mount(ui.default, { target: document.getElementById('app') })
  const labels = () => [...document.querySelectorAll('.agent .label')].map((n) => n.textContent)
  try {
    ui.flushSync()
    document.querySelectorAll<HTMLElement>('.phases button.row')[0]?.click()
    ui.flushSync()
    assert.deepEqual(labels(), ['T1', 'T4'])
    ui.flushSync(() => app.dropAgent(1))
    assert.deepEqual(labels(), ['T4'], 'the Plan row stays open on its remaining agent')
  } finally {
    await ui.unmount(app); dom.window.close()
    for (const name of domNames) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})

test('an open phase row stays open when it gains an agent and the window then trims the old ones', async () => {
  const dom = installDom()
  const ui = await importBundle(plain.contents, 'RunFixture')
  const app = ui.mount(ui.default, { target: document.getElementById('app') })
  const labels = () => [...document.querySelectorAll('.agent .label')].map((n) => n.textContent)
  try {
    ui.flushSync()
    document.querySelectorAll<HTMLElement>('.phases button.row')[0]?.click()
    ui.flushSync()
    ui.flushSync(() => app.addAgent(5, 'Plan'))
    assert.deepEqual(labels(), ['T1', 'T4', 'T5'])
    ui.flushSync(() => app.dropAgent(1)); ui.flushSync(() => app.dropAgent(4))
    assert.deepEqual(labels(), ['T5'], 'the Plan row stays open on the agent it gained')
  } finally {
    await ui.unmount(app); dom.window.close()
    for (const name of domNames) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})

test('opening an agent card shows its Session button in the body', async () => {
  const dom = installDom()
  const ui = await importBundle(plain.contents, 'RunFixture')
  const app = ui.mount(ui.default, { target: document.getElementById('app') })
  try {
    ui.flushSync()
    document.querySelector<HTMLElement>('.phases button.row')?.click()
    ui.flushSync()
    assert.equal(document.querySelectorAll('.agent .session').length, 0, 'a closed card has no Session button')
    document.querySelector<HTMLElement>('.agent button.head')?.click()
    ui.flushSync()
    assert.equal(document.querySelectorAll('.agent .full .session').length, 1)
  } finally {
    await ui.unmount(app); dom.window.close()
    for (const name of domNames) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})
