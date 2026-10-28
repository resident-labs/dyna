import test, { after } from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { domNames, installDom } from '../prs/prDetailHarness.ts'

const dom = installDom()
for (const name of ['DOMParser', 'NodeFilter']) Reflect.set(globalThis, name, Reflect.get(dom.window, name))
after(() => {
  dom.window.close()
  for (const name of [...domNames, 'DOMParser', 'NodeFilter']) Reflect.deleteProperty(globalThis, name)
})

const compiled = await build({
  bundle: true, write: false, platform: 'node', format: 'esm', conditions: ['node'], logLevel: 'silent',
  stdin: { contents: "export { default as Feed } from './rooms/Feed.svelte'; export { render } from 'svelte/server'", resolveDir: new URL('..', import.meta.url).pathname },
  plugins: [svelteEsbuildPlugin('server')],
})
const output = compiled.outputFiles?.[0]
assert.ok(output)
const { Feed, render } = await import('data:text/javascript;base64,' + Buffer.from(output.contents).toString('base64'))
const room = { id: 'room', name: 'room' }

test('an empty room guides profile setup and shows one enabled profile command', () => {
  const noProfile = render(Feed, { props: { room, posts: [], profiles: [], onnewprofile() {} } }).body
  assert.match(noProfile, /Start your first session/)
  assert.match(noProfile, /New profile/)
  assert.doesNotMatch(noProfile, /No messages yet/)

  const ready = render(Feed, { props: { room, posts: [], profiles: [
    { name: 'worker', harness: 'mock' },
    { name: 'default', harness: 'mock', default: true },
  ], onnewprofile() {} } }).body
  assert.match(ready, /\/default Review this project and suggest a first task\./)
  assert.doesNotMatch(ready, /\/worker Review/)
  assert.doesNotMatch(ready, /New profile/)
})


test('turn results and ordinary posts expose the same actions', () => {
  const common = { ts: '2026-10-20T08:56:00Z', from: 'room:worker', to: ['room'], owner: false, completion: false, fromHue: 1, speaker: 'worker', text: 'hello' }
  const post = { ...common, id: 'ordinary', kind: 'message' }
  const turn = { ...common, id: 'turn', kind: 'turn', completion: true, turn: 1, notice: { kind: 'completed' }, payload: { session: 'room:worker', sessionId: 'ses_worker', turn: 1, state: 'completed', summary: 'hello' } }
  const body = render(Feed, { props: { room, posts: [post, turn], onreply() {}, onforward() {}, onpin() {} } }).body
  for (const label of ['Reply', 'Forward', 'Copy text', 'Copy link', 'Pin']) {
    assert.equal((body.match(new RegExp(`aria-label="${label}"`, 'g')) ?? []).length, 2, label)
  }
})
