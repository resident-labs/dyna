import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { build } from 'esbuild'
import { compileSvelte, svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { topLevelFunctions } from '../../../test/svelte-source.ts'
import { domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'
import { post, session } from '../../../test/session-fixtures.ts'

const app = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const callback = (component: string) => {
  const line = app.split('\n').find((line) => line.includes(`<${component} `))
  const expression = line?.match(/onpost=\{(.*?)\}/)?.[1]
  assert.ok(expression, component + ' forwards original-post navigation')
  return expression
}
const fixture = `<script>
  import { tick } from 'svelte';
  import Session from './Session.svelte';
  import SessionMedia from './SessionMedia.svelte';
  let { room, session, post, media } = $props();
  let roomId = room.id, detailRequest = 0, drill = null, card = null, viewing = null, feedAtBottom = true, view = '', openJob = '';
  // The screen state lives in the router now; the sliced jumpTo writes it there.
  const router = { get roomId() { return roomId }, set roomId(v) { roomId = v }, get view() { return view }, set view(v) { view = v }, get drill() { return drill }, set drill(v) { drill = v }, get detailRequest() { return detailRequest }, set detailRequest(v) { detailRequest = v }, get openJob() { return openJob }, set openJob(v) { openJob = v } };
  const feed = { posts: [post] }, jumps = [], feedView = { jump: id => jumps.push(id) };
  const GENERAL = 'general', VOICE = 'voice-call';
  const clearItemLink = () => {}, loadEarlier = async () => false, questionID = () => '', openQuestion = () => {}, isShellResult = () => false, inView = () => false, homeView = () => GENERAL;
  ${topLevelFunctions(app, ['jumpTo']).get('jumpTo')}
  export function reached() { return [...jumps] }
</script>
{#if media}<SessionMedia {room} posts={[post]} onopenfile={() => {}} onpost={${callback('SessionMedia')}} />
{:else}<Session {room} {session} onopenfile={() => {}} onsend={async () => undefined} onchild={() => {}} onpr={() => {}} onjob={() => {}} onpost={${callback('Session')}} />{/if}`
const result = await build({
  bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'], logLevel: 'silent',
  stdin: { contents: "export {default} from 'navigation-fixture'; export {mount,unmount,flushSync} from 'svelte'", resolveDir: new URL('.', import.meta.url).pathname },
  plugins: [{ name: 'navigation-fixture', setup(builder) {
    builder.onResolve({ filter: /^navigation-fixture$/ }, () => ({ path: 'navigation-fixture', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, () => ({ contents: compileSvelte(fixture, 'NavigationFixture.svelte'), resolveDir: new URL('.', import.meta.url).pathname }))
  } }, svelteEsbuildPlugin()],
})
const code = result.outputFiles[0]?.contents
assert.ok(code)

test('session media and transcript post links reach App with the original post ID', async () => {
  const dom = installDom()
  const extra = ['DOMParser', 'NodeFilter', 'HTMLMediaElement', 'HTMLTextAreaElement']
  for (const name of extra) Reflect.set(globalThis, name, Reflect.get(dom.window, name))
  const savedMedia = globalThis.matchMedia
  globalThis.matchMedia = () => ({ matches: false, media: '', onchange: null, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {}, dispatchEvent() { return true } })
  const proto = dom.window.HTMLElement.prototype
  for (const name of ['offsetHeight', 'clientHeight']) Object.defineProperty(proto, name, { configurable: true, get() { return this.classList.contains('vlist') ? parseFloat(this.style.height) || 0 : 500 } })
  for (const name of ['offsetWidth', 'clientWidth']) Object.defineProperty(proto, name, { configurable: true, get: () => 800 })
  proto.getBoundingClientRect = function () { return new dom.window.DOMRect(0, 0, 800, this.offsetHeight) }
  Reflect.set(proto, 'scrollTo', function (this: HTMLElement, options: ScrollToOptions) { this.scrollTop = options.top ?? 0 })
  Reflect.set(globalThis, 'ResizeObserver', class {
    callback: (entries: { target: Element; borderBoxSize: { inlineSize: number; blockSize: number }[] }[]) => void
    constructor(callback: (entries: { target: Element; borderBoxSize: { inlineSize: number; blockSize: number }[] }[]) => void) { this.callback = callback }
    observe(element: Element) { this.callback([{ target: element, borderBoxSize: [{ inlineSize: 800, blockSize: 500 }] }]) }
    unobserve() {}
    disconnect() {}
  })
  const mod = await importBundle(code, 'NavigationFixture')
  try {
    const original = post('msg_original', { ts: Date.now(), from: 'test', owner: true, human: true, to: ['worker'], attachments: [{ id: 'image', name: 'image.png', mime: 'image/png', size: 1, sha256: 'test' }] })
    for (const media of [true, false]) {
      const instance = mod.mount(mod.default, { target: document.body, props: { room: { id: 'room_20261017-100000-aaaaaaaa', name: 'test' }, session: session('worker', { posts: [original] }), post: original, media } })
      try {
      mod.flushSync()
      await new Promise(setImmediate)
      mod.flushSync()
      const link = document.querySelector<HTMLAnchorElement>('a[title="Open original room post"]')
      assert.ok(link, media ? 'media link' : 'transcript timestamp link')
      link.click()
      await new Promise(setImmediate)
      assert.deepEqual(instance.reached(), [original.id])
      } finally { await mod.unmount(instance) }
    }
  } finally {
    await rm(mod.dir, { recursive: true, force: true })
    dom.window.close()
    globalThis.matchMedia = savedMedia
    for (const name of [...domNames, ...extra]) Reflect.deleteProperty(globalThis, name)
  }
})
