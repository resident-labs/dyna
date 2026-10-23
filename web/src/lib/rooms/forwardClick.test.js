import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { dirname } from 'node:path'
import { build } from 'esbuild'
import { stripTypes } from '../../../test/svelte-source.ts'
import { parse } from 'svelte/compiler'
import { domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'
import { compileSvelte, svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'

const app = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const handlers = parse(app).instance.content.body.filter((n) => n.type === 'FunctionDeclaration' && n.id.name === 'sendToSession').map((n) => stripTypes(app.slice(n.start, n.end))).join('\n')
const callback = app.match(/<Feed[^\n]*onforward=\{([^}]+)\}/)?.[1]

// Bundle outside the test clock: only the mounted flow is measured.
const source = `<script>
  import Session from '../sessions/Session.svelte'; import { api } from '../../api.js';
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
const flowCode = result.outputFiles[0].contents

for (const view of ['DM thread']) {
  test(`${view}: Forward adds a removable composer quote and normal Send carries its source ID`, async () => {
    const dom = installDom()
    const extraNames = ['DOMParser', 'NodeFilter', 'HTMLMediaElement', 'HTMLTextAreaElement']
    for (const name of extraNames) globalThis[name] = dom.window[name]
    const savedFetch = globalThis.fetch
    const savedMedia = globalThis.matchMedia
    globalThis.matchMedia = () => ({ matches: true, addEventListener() {}, removeEventListener() {} })
    const room = { id: 'room_test', name: 'alpha' }
    const original = { id: 'msg_original', from: 'alpha:peer', text: 'Original result', ts: Date.now(), to: ['alpha'] }
    const member = { id: 'worker', name: 'worker', address: 'alpha:worker', state: 'idle' }
    const forwarded = { id: 'msg_forward', from: 'alpha', human: true, owner: true, ts: Date.now(), to: ['alpha:worker'], text: 'Please check', forward: { ...original, roomId: room.id } }
    assert.ok(callback, 'App wires the channel Forward action')
    let instance, unmount, dir
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
      const calls = []
      let rejectSend = true
      globalThis.fetch = async (url, options) => {
        const args = JSON.parse(options.body)
        if (url === '/api/Send' || url === '/api/SendToSession') {
          calls.push({ url, args })
          if (rejectSend) return { ok: false, status: 400, json: async () => ({ error: 'Try again' }) }
        }
        const value = url === '/api/Send' ? { kind: 'post', post: { message: forwarded } } : url === '/api/SendToSession' ? { message: forwarded } : {}
        return { ok: true, status: 200, json: async () => value }
      }
      instance = mod.mount(mod.default, { target: document.getElementById('app'), props: { room, original, member } })
      flushSync()
      const clickForward = () => {
        document.querySelector('.thread-chip').click()
        flushSync()
        const button = document.querySelector('button[title="Forward"]')
        assert.ok(button, `${view} row has a Forward action`)
        button.click(); flushSync()
      }
      clickForward()
      assert.equal(document.querySelector('.thread-panel'), null, 'Forward reveals the composer')
      assert.equal(document.querySelector('[role="dialog"]'), null)
      assert.match(document.querySelector('.composer').textContent, /Forwarding a post/)
      assert.match(document.querySelector('.composer').textContent, /Original result/)
      assert.equal(document.querySelector('button[aria-label="Send"]').disabled, false, 'a quote alone enables Send')
      document.querySelector('button[aria-label="Remove forwarded post"]').click()
      flushSync()
      assert.doesNotMatch(document.querySelector('.composer').textContent, /Forwarding a post/)
      assert.equal(document.querySelector('button[aria-label="Send"]').disabled, true)
      clickForward()
      const input = document.querySelector('textarea')
      input.value = 'Please check'
      input.dispatchEvent(new Event('input', { bubbles: true }))
      flushSync()
      document.querySelector('button[aria-label="Send"]').click()
      await new Promise(setImmediate)
      flushSync()
      assert.match(document.querySelector('.composer').textContent, /Try again/)
      assert.match(document.querySelector('.composer').textContent, /Forwarding a post/)
      assert.equal(input.value, 'Please check', 'a failed send keeps the note and quote')
      rejectSend = false
      document.querySelector('button[aria-label="Send"]').click()
      await new Promise(setImmediate)
      flushSync()
      const expected = { url: '/api/SendToSession', args: [member.id, 'Please check', [], original.id] }
      assert.deepEqual(calls, [expected, expected])
      assert.doesNotMatch(document.querySelector('.composer').textContent, /Forwarding a post/)
      instance.deliver(forwarded)
      flushSync()
      assert.match(document.body.textContent, /Forwarded from/)
      assert.equal(document.querySelector('.fwdtext').textContent.trim(), original.text)
    } finally {
      if (instance) await unmount(instance)
      dom.window.close()
      globalThis.fetch = savedFetch
      if (savedMedia) globalThis.matchMedia = savedMedia
      else delete globalThis.matchMedia
      for (const name of [...domNames, ...extraNames]) delete globalThis[name]
      if (dir) await rm(dir, { recursive: true, force: true })
    }
  })
}
