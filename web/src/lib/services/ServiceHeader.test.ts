import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { compile } from 'svelte/compiler'
import { build } from 'esbuild'
import type { ServiceView } from '../api/service.ts'
import { serviceStatus, pinNotEnabled, pickProfile } from './services.ts'

// Render the real ServiceHeader on the server and read the text.
async function load() {
  const dir = await mkdtemp(join(tmpdir(), 'dyna-service-header-'))
  const result = await build({
    bundle: true, write: false,
    stdin: { contents: `export { default } from './ServiceHeader.svelte'; export { render } from 'svelte/server'`, resolveDir: new URL('.', import.meta.url).pathname, loader: 'js' },
    format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
    plugins: [{ name: 'svelte-test', setup(builder) {
      builder.onLoad({ filter: /\.svelte$/ }, async ({ path }) => ({ contents: compile(readFileSync(path, 'utf8'), { filename: path, generate: 'server' }).js.code, loader: 'js', resolveDir: resolve(path, '..') }))
      builder.onLoad({ filter: /[/\\]api\.ts$/ }, () => ({ contents: 'export const api = {}', loader: 'js' }))
    } }],
  })
  const output = result.outputFiles[0]
  if (!output) throw new Error('esbuild wrote no output')
  const file = join(dir, 'ServiceHeader.mjs')
  await writeFile(file, output.contents)
  const module = await import(pathToFileURL(file).href)
  return { dir, component: module.default, render: module.render }
}

// profiles is what the engine says the service can run, services-only profiles included.
const steward: ServiceView = { role: 'steward', name: 'Merge steward', page: 'goals', profile: 'sol', pinned: '', source: 'auto', reason: 'auto: best taste + cost', scope: 'global', sessions: 1, profiles: ['sol', 'luna'] }
const reviewer: ServiceView = { ...steward, role: 'reviewer', name: 'PR reviewer', page: 'pull-requests', profile: 'luna', pinned: 'luna', source: 'pinned', reason: 'pinned by the room', scope: 'room', sessions: 2 }
const text = (html: string) => html.replace(/<!--.*?-->/g, '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ')

test('the status line names the profile, the reason and the open sessions', () => {
  assert.equal(serviceStatus(steward).text, 'sol · auto: best taste + cost · 1 session')
  assert.equal(serviceStatus(reviewer).text, 'luna · pinned by the room · 2 sessions')
  assert.deepEqual(serviceStatus({ ...steward, profile: '', reason: 'no enabled steward profile is available' }), { text: 'no enabled steward profile is available', tone: 'coral' })
  assert.deepEqual(serviceStatus(null), { text: '', tone: '' })
})

test('the header is one line: no scope chip, a plain note only for a service of all rooms', async () => {
  const { dir, component, render } = await load()
  try {
    const show = (service: ServiceView) => render(component, { props: { roomId: 'r', role: service.role, service } }).body
    const auto = show(steward)
    assert.match(text(auto), /Merge steward sol · auto: best taste \+ cost · 1 session Applies to all rooms Auto sol luna/)
    assert.match(auto, /aria-label="Merge steward profile"/)
    assert.match(auto, /<option value="" selected/)
    const pinned = show(reviewer)
    assert.match(text(pinned), /PR reviewer luna · pinned by the room · 2 sessions Auto sol luna/)
    assert.match(pinned, /<option value="luna" selected/)
    // A pin to a profile that is no longer enabled stays visible.
    assert.match(text(show({ ...reviewer, pinned: 'old' })), /Auto old \(not enabled\) sol luna/)
    // A pin to a services-only profile, such as sol, is enabled for the service.
    const sol = show({ ...reviewer, pinned: 'sol', profiles: ['sol'] })
    assert.doesNotMatch(sol, /not enabled/)
    assert.match(sol, /<option value="sol" selected/)
    assert.equal(text(render(component, { props: { roomId: 'r', role: 'steward' } }).body).trim(), '')
  } finally {
    await rm(dir, { recursive: true, force: true })
  }
})

test('a pin is "not enabled" only when the service cannot run it', () => {
  assert.equal(pinNotEnabled('old', ['sol', 'luna']), true)
  assert.equal(pinNotEnabled('sol', ['sol', 'luna']), false)
  assert.equal(pinNotEnabled('', ['sol']), false)
})

test('a failed save puts the menu back on the saved pin', async () => {
  const select = { value: 'nova' }
  const error = await pickProfile(select, async () => { throw new Error('no enabled profile named "nova"') }, 'luna')
  assert.equal(error, 'no enabled profile named "nova"')
  assert.equal(select.value, 'luna')
  const saved: string[] = []
  assert.equal(await pickProfile(select, async (p) => saved.push(p), 'luna'), '')
  assert.deepEqual(saved, ['luna'])
})
