import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { dirname } from 'node:path'
import { build } from 'esbuild'
import { stripTypes, sourceRange } from '../../../test/svelte-source.ts'
import { parse } from 'svelte/compiler'
import { domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'
import { compileSvelte, svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'

// The tests read these control properties from the rendered page.
type PageElement = HTMLElement & { value: string; disabled: boolean }
function queryElement(selector: string): PageElement {
  const element = document.querySelector<PageElement>(selector)
  assert.ok(element, selector)
  return element
}
const textOf = (selector: string): string => queryElement(selector).textContent ?? ''

const app = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const handlers = (parse(app, { modern: true }).instance?.content.body ?? []).filter((n) => n.type === 'FunctionDeclaration' && n.id?.name === 'sendToSession').map((n) => {
  const { start, end } = sourceRange(n)
  return stripTypes(app.slice(start, end))
}).join('\n')
const callback = app.match(/<Feed[^\n]*onforward=\{([^}]+)\}/)?.[1]

// Bundle outside the test clock: only the mounted flow is measured.
const source = `<script>
  import Session from '../sessions/Session.svelte'; import { api } from '../../api.ts';
  let { room, original, member } = $props();
  let roomId = room.id, view = 'general', reply = null, fresh = '';
  let draft = $state(''), drafts = $state({}), composer = $state();
  let posts = $state([original]);
  let session = $state({ member, posts: [original], turns: [] });
  let members = [member, { name: 'root' }], drill = member;
  const openSession = () => {}, loadMembers = () => {};
  ${handlers}
  export function deliver(post) { posts = [...posts, post]; session = { ...session, posts: [...session.posts, post] } }
</script>
<Session {room} {session} {members} onsend={sendToSession} />`
const result = await build({
  bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'], logLevel: 'silent',
  stdin: { contents: "export { default } from 'forward-flow'; export { mount, flushSync, unmount } from 'svelte'", resolveDir: dirname(new URL(import.meta.url).pathname) },
  plugins: [svelteEsbuildPlugin(), { name: 'forward-flow', setup(builder) {
    builder.onResolve({ filter: /^forward-flow$/ }, () => ({ path: 'forward-flow', namespace: 'flow' }))
    builder.onLoad({ filter: /.*/, namespace: 'flow' }, () => ({ contents: compileSvelte(source, 'ForwardFlow.svelte'), resolveDir: dirname(new URL(import.meta.url).pathname) }))
  } }],
})
const output = result.outputFiles?.[0]
assert.ok(output, 'the forward flow built')
const flowCode = output.contents

interface ForwardedPost {
  id: string
  from: string
  human?: boolean
  owner?: boolean
  ts: number
  to: string[]
  text: string
  forward?: unknown
}

interface FlowInstance {
  deliver(post: ForwardedPost): void
}

for (const view of ['DM thread']) {
  test(`${view}: Forward adds a removable composer quote and normal Send carries its source ID`, async () => {
    const dom = installDom()
    const extraNames = ['DOMParser', 'NodeFilter', 'HTMLMediaElement', 'HTMLTextAreaElement']
    Object.assign(globalThis, Object.fromEntries(extraNames.map((name) => [name, Reflect.get(dom.window, name)])))
    const savedFetch = globalThis.fetch
    const savedMedia = globalThis.matchMedia
    Reflect.set(globalThis, 'matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
    const room = { id: 'room_test', name: 'alpha' }
    const original = { id: 'msg_original', from: 'alpha:peer', text: 'Original result', ts: Date.now(), to: ['alpha'] }
    const member = { id: 'worker', name: 'worker', address: 'alpha:worker', state: 'idle' }
    const forwarded = { id: 'msg_forward', from: 'alpha', human: true, owner: true, ts: Date.now(), to: ['alpha:worker'], text: 'Please check', forward: { ...original, roomId: room.id } }
    assert.ok(callback, 'App wires the channel Forward action')
    let instance: FlowInstance | undefined, unmount: ((instance: FlowInstance) => Promise<void>) | undefined, dir: string | undefined
    try {
      const mod = await importBundle(flowCode, 'ForwardFlow')
      dir = mod.dir
      unmount = mod.unmount
      const { flushSync } = mod
      globalThis.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} }
      dom.window.ResizeObserver = globalThis.ResizeObserver
      dom.window.HTMLElement.prototype.scrollTo = function () {}
      for (const name of ['offsetHeight', 'clientHeight']) Object.defineProperty(dom.window.HTMLElement.prototype, name, { get() { return 700 } })
      for (const name of ['offsetWidth', 'clientWidth']) Object.defineProperty(dom.window.HTMLElement.prototype, name, { get() { return 1000 } })
      const calls: { url: string; args: unknown[] }[] = []
      let rejectSend = true
      // The page's fetch takes the app's JSON request and answers with its JSON body.
      Reflect.set(globalThis, 'fetch', async (url: string, options: { body: string }) => {
        const args: unknown[] = JSON.parse(options.body)
        if (url === '/api/Send' || url === '/api/SendToSession') {
          calls.push({ url, args })
          if (rejectSend) return { ok: false, status: 400, json: async () => ({ error: 'Try again' }) }
        }
        const value = url === '/api/Send' ? { kind: 'post', post: { message: forwarded } } : url === '/api/SendToSession' ? { message: forwarded } : {}
        return { ok: true, status: 200, json: async () => value }
      })
      instance = mod.mount(mod.default, { target: document.getElementById('app'), props: { room, original, member } })
      flushSync()
      const clickForward = () => {
        queryElement('.thread-chip').click()
        flushSync()
        const button = document.querySelector<PageElement>('button[title="Forward"]')
        assert.ok(button, `${view} row has a Forward action`)
        button.click(); flushSync()
      }
      clickForward()
      assert.equal(document.querySelector('.thread-panel'), null, 'Forward reveals the composer')
      assert.equal(document.querySelector('[role="dialog"]'), null)
      assert.match(textOf('.composer'), /Forwarding a post/)
      assert.match(textOf('.composer'), /Original result/)
      assert.equal(queryElement('button[aria-label="Send"]').disabled, false, 'a quote alone enables Send')
      queryElement('button[aria-label="Remove forwarded post"]').click()
      flushSync()
      assert.doesNotMatch(textOf('.composer'), /Forwarding a post/)
      assert.equal(queryElement('button[aria-label="Send"]').disabled, true)
      clickForward()
      const input = queryElement('textarea')
      input.value = 'Please check'
      input.dispatchEvent(new Event('input', { bubbles: true }))
      flushSync()
      queryElement('button[aria-label="Send"]').click()
      await new Promise(setImmediate)
      flushSync()
      assert.match(textOf('.composer'), /Try again/)
      assert.match(textOf('.composer'), /Forwarding a post/)
      assert.equal(input.value, 'Please check', 'a failed send keeps the note and quote')
      rejectSend = false
      queryElement('button[aria-label="Send"]').click()
      await new Promise(setImmediate)
      flushSync()
      const expected = { url: '/api/SendToSession', args: [member.id, 'Please check', [], original.id] }
      assert.deepEqual(calls, [expected, expected])
      assert.doesNotMatch(textOf('.composer'), /Forwarding a post/)
      instance?.deliver(forwarded)
      flushSync()
      assert.match(document.body.textContent ?? '', /Forwarded from/)
      assert.equal(queryElement('.fwdtext').textContent?.trim(), original.text)
    } finally {
      if (instance) await unmount?.(instance)
      dom.window.close()
      globalThis.fetch = savedFetch
      if (savedMedia) globalThis.matchMedia = savedMedia
      else Reflect.deleteProperty(globalThis, 'matchMedia')
      for (const name of [...domNames, ...extraNames]) Reflect.deleteProperty(globalThis, name)
      if (dir) await rm(dir, { recursive: true, force: true })
    }
  })
}
