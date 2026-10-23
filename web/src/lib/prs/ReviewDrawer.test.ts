import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { compile, compileModule } from 'svelte/compiler'
import { build } from 'esbuild'
import type { Facts } from './pr.ts'
import { need } from '../../../test/need.ts'

// Render the real ReviewDrawer, PRChip and PR page on the server
// and read the text. The page's ServiceHeader is a stub that prints its props.
async function load(name: string, width = 1200) {
  const dir = await mkdtemp(join(tmpdir(), 'dyna-reviewer-ui-'))
  const result = await build({
    bundle: true, write: false,
    stdin: { contents: `export { default } from './${name}.svelte'; export { render } from 'svelte/server'`, resolveDir: new URL('.', import.meta.url).pathname, loader: 'js' },
    format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
    plugins: [{ name: 'svelte-test', setup(builder) {
      builder.onLoad({ filter: /[/\\]ServiceHeader\.svelte$/ }, ({ path }) => ({ contents: compile('<script>let { roomId, role, onchange } = $props()</script><b class="service-header">{role} in {roomId} {typeof onchange}</b>', { filename: path, generate: 'server' }).js.code, loader: 'js', resolveDir: resolve(path, '..') }))
      builder.onLoad({ filter: /\.svelte\.js$/ }, ({ path }) => ({ contents: compileModule(readFileSync(path, 'utf8'), { filename: path, generate: 'server' }).js.code, loader: 'js', resolveDir: resolve(path, '..') }))
      builder.onLoad({ filter: /\.svelte$/ }, async ({ path }) => {
        let source = readFileSync(path, 'utf8')
        // SSR has no ResizeObserver. Supply the browser's measured PR width.
        if (path.endsWith('/GitHub.svelte')) source = source.replace('let width = $state(1200)', `let width = $state(${width})`)
        return { contents: compile(source, { filename: path, generate: 'server' }).js.code, loader: 'js', resolveDir: resolve(path, '..') }
      })
      builder.onLoad({ filter: /[/\\]api\.js$/ }, () => ({ contents: 'export const api = {}; export const copyText = async () => {}; export const openExternal = () => {}', loader: 'js' }))
    } }],
  })
  const file = join(dir, `${name}.mjs`)
  await writeFile(file, need(result.outputFiles?.[0]).contents)
  const module = await import(pathToFileURL(file).href)
  return { dir, component: module.default, render: module.render }
}

const base = { enabled: true, session: { id: 's1', address: 'room:reviewer', status: 'idle' }, profile: 'sol', profileSource: 'auto', profileReason: 'auto: best taste + cost', current: null, queue: [], lastError: '', recent: [] }
const members = [{ id: 's1', name: 'reviewer', hue: 3 }]
const text = (html: string) => html.replace(/<!--.*?-->/g, '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ')

test('the drawer holds only one change\'s own rules; room settings moved out', async () => {
  const { dir, component, render } = await load('ReviewDrawer')
  try {
    const words = text(render(component, { props: { roomId: 'r', change: { changeId: 'c1', synthetic: true } } }).body)
    assert.match(words, /Rules for this change/)
    assert.match(words, /The room rules are in Room settings/)
    assert.doesNotMatch(words, /Automatic review|Restart reviewer|Review starts after|CI time budget/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})

test('PR chip carries the review state label, or the row chip it is given', async () => {
  const { dir, component, render } = await load('PRChip')
  try {
    const chip = (pr: Facts, meta?: object) => text(render(component, { props: { pr: { number: 9, status: 'awaiting-review', ...pr }, meta } }).body)
    assert.match(chip({ reviewState: 'queued', queuePosition: 2 }), /#9 Queued for review \(2nd\)/)
    assert.match(chip({ reviewState: 'blocked', status: 'paused', reviewBlocked: 'All profiles are failing' }), /All profiles are failing/)
    assert.match(chip({ reviewState: 'approved', status: 'approved', ci: 'passed' }), /Approved: ready to merge/)
    assert.match(chip({ reviewState: 'approved' }, { label: 'Ready to merge', tone: 'sage' }), /Ready to merge/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})

test('the PR page has no reviewer bar and no review strip at the top', async () => {
  const { dir, component, render } = await load('GitHub')
  try {
    const html = render(component, { props: { roomId: 'room-1', reviewer: base, members } }).body
    const words = text(html)
    // The reviewer profile is chosen in room settings.
    assert.doesNotMatch(words, /reviewer in room-1/)
    assert.doesNotMatch(words, /Auto review|⚙ Settings/)
    assert.doesNotMatch(html, /role="switch"/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})

 test('PR chips use the shared settled outlines and running dot', async () => {
  const { dir, component, render } = await load('PRChip')
  try {
   const show = (tone: string, props: object = {}) => render(component, { props: { pr: { number: 9, title: 'Example' }, meta: { tone, label: 'State' }, ...props } }).body
   assert.match(show('done'), /class="chip sage[^"]*outline/)
   assert.match(show('faint'), /class="chip [^"]*outline/)
   assert.match(show('run'), /class="dot /)
   assert.match(show('iris', { more: 2 }), /class="chip iris/)
   assert.match(text(show('iris', { more: 2 })), /#9 State \+2/)
   assert.doesNotMatch(text(show('sage', { short: true, number: false })), /#9|State/)
  } finally { await rm(dir, { recursive: true, force: true }) }
 })


test('a narrow PR page retains its hidden list for Back navigation', async () => {
  const { dir, component, render } = await load('GitHub', 393)
  try {
    const row = { changeId: 'repo#17', key: 'repo#17', kind: 'github', number: 17, title: 'Keep the list position', state: 'open', status: 'ci-running' }
    const html = render(component, { props: { roomId: 'room-1', changes: [row], link: { pr: '17', tab: 'conversation' } } }).body
    assert.match(html, /class="list[^"]*" hidden/, 'the list stays mounted and hidden while the PR page shows')
    assert.match(html, /class="rows[^"]*"/, 'the actual list rows stay mounted with their scroll container')
    assert.match(html, /class="detail[^"]*"/, 'the selected PR page also renders')
  } finally { await rm(dir, { recursive: true, force: true }) }
})
