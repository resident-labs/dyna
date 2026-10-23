import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { compile, compileModule } from 'svelte/compiler'
import { build } from 'esbuild'
import { JSDOM } from 'jsdom'
import { need } from '../../../test/need.ts'

interface Recorded {
  text: string
  duration: number
  moves: boolean
}
interface FakeAnimation {
  currentTime: number
  onfinish: (() => void) | null
  cancel(): void
  finished: Promise<void>
}

// PRList compiled for the browser, mounted in jsdom.
// A harness module holds the reactive props, so the test can move a PR.
async function load() {
  const dir = await mkdtemp(join(tmpdir(), 'dyna-pr-crossfade-'))
  const harness = join(dir, 'harness.svelte.js')
  await writeFile(harness, `import PRList from ${JSON.stringify(new URL('./PRList.svelte', import.meta.url).pathname)}
import { mount, flushSync } from 'svelte'
export const props = $state({ queue: [], groups: [], states: {}, onpick() {} })
export function start(target) { mount(PRList, { target, props }) }
export { flushSync }`)
  const result = await build({
    bundle: true, write: false, entryPoints: [harness], nodePaths: [new URL('../../../node_modules', import.meta.url).pathname],
    format: 'esm', platform: 'node', conditions: ['browser'], logLevel: 'silent',
    plugins: [{ name: 'svelte-test', setup(builder) {
      builder.onLoad({ filter: /\.svelte\.js$/ }, ({ path }) => ({ contents: compileModule(readFileSync(path, 'utf8'), { filename: path }).js.code, loader: 'js', resolveDir: resolve(path, '..') }))
      builder.onLoad({ filter: /\.svelte$/ }, ({ path }) => ({ contents: compile(readFileSync(path, 'utf8'), { filename: path }).js.code, loader: 'js', resolveDir: resolve(path, '..') }))
      builder.onLoad({ filter: /[/\\]format\.ts$/ }, () => ({ contents: "export const markdown = (t) => t || ''; export const ago = () => 'now'; export const initials = (n) => n.slice(0, 2); export const short = (a) => a; export const clock = () => ''; export const stamp = () => '';", loader: 'js' }))
    } }],
  })
  const file = join(dir, 'PRList.mjs')
  await writeFile(file, need(result.outputFiles?.[0]).contents)
  return { dir, ...(await import(pathToFileURL(file).href)) }
}
// Let queued microtasks run: Svelte starts transitions in a microtask.
const microtasks = async () => { for (let i = 0; i < 5; i++) await Promise.resolve() }

test('the sole PR crossfades into an empty merge queue and back to its stage', async () => {
  const dom = new JSDOM('<!doctype html><div id="app"></div>', { pretendToBeVisual: true, url: 'http://localhost/' })
  const w = dom.window
  const animations: Recorded[] = [], pending: FakeAnimation[] = []
  // jsdom has no Web Animations. The mock records each animation and leaves it
  // running; the test finishes them itself, so nothing depends on timers.
  Object.assign(w.Element.prototype, { animate(this: Element, keyframes: unknown, options?: { duration?: number }) {
    animations.push({ text: this.textContent ?? '', duration: options?.duration ?? 0, moves: JSON.stringify(keyframes ?? '').includes('translate(') })
    const a: FakeAnimation = { currentTime: 0, onfinish: null, cancel() {}, finished: Promise.resolve() }
    pending.push(a)
    return a
  } })
  const names = ['window', 'document', 'Element', 'Node', 'HTMLElement', 'Event', 'MouseEvent', 'DocumentFragment', 'CharacterData', 'Text', 'Comment', 'HTMLTemplateElement', 'CustomEvent', 'getComputedStyle', 'requestAnimationFrame']
  Object.assign(globalThis, Object.fromEntries(names.map((n) => [n, n === 'window' ? w : Reflect.get(w, n)])))
  const { dir, props, start, flushSync } = await load()
  try {
    const pr = { changeId: 'c1', key: 'c1', kind: 'github', number: 1, title: 'Only PR', state: 'open', status: 'approved', ci: 'passed', checks: [] }
    const state = { id: 'next', label: 'Next', tone: 'sage' }
    const inStage = { queue: [], groups: [{ id: 'progress', label: 'In progress', rows: [pr] }], states: { c1: state } }
    const inQueue = { queue: [{ row: pr, item: {}, state }], groups: [], states: {} }
    Object.assign(props, inStage)
    start(need(document.getElementById('app'), '#app'))
    flushSync()
    assert.equal(animations.length, 0, 'the first render does not animate')
    // step finishes the running animations once; Svelte first runs a
    // zero-length delay animation and starts the real one when it ends.
    const step = async () => {
      for (const a of pending.splice(0)) a.onfinish?.()
      flushSync(); await microtasks()
    }
    // finish completes every animation, and any it starts in turn.
    const finish = async () => { while (pending.length) await step() }
    for (const next of [inQueue, inStage]) {
      animations.length = 0
      Object.assign(props, next)
      flushSync(); await microtasks()
      await step()
      assert.ok(animations.some((a) => a.moves && a.duration === 200 && a.text.includes('Only PR')), `the moved row crossfades: ${JSON.stringify(animations)}`)
      await finish()
    }
  } finally { await rm(dir, { recursive: true, force: true }) }
})
