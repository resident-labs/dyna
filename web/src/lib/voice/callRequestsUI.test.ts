import assert from 'node:assert/strict'
import test, { after } from 'node:test'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { installDom, domNames } from '../prs/prDetailHarness.ts'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import type { CallState } from './callRequests.ts'

// Keep the real request store and widget. Replace only the engine and
// microphone boundary, so clicks cannot start a real call in this fixture.
const result = await build({
  bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser'], logLevel: 'silent',
  stdin: {
    contents: `export { default } from './VoiceWidget.svelte'
      export { default as Row } from './CallRequestRow.svelte'
      export { calls, callRequests } from './voiceCall.ts'
      export { mount, unmount, flushSync } from 'svelte'`,
    resolveDir: new URL('.', import.meta.url).pathname, loader: 'js',
  },
  plugins: [svelteEsbuildPlugin(), { name: 'call-fixture', setup(b) {
    b.onLoad({ filter: /[/\\]voiceCall\.ts$/ }, ({ path }) => ({
      contents: `import { writable } from 'svelte/store'
        import { initial } from './callState.ts'
        import { createCallRequests } from './callRequests.ts'
        export const calls = []
        export const callRequests = createCallRequests({
          callRequests: async () => ({}),
          callRequestShown: async (room, id) => { calls.push(['shown', room, id]) },
          declineCallRequest: async (room, id) => { calls.push(['decline', room, id]); return { id, room, reason: 'CI needs your sign-in', state: 'declined', askedAt: '2026-10-20T10:00:00Z' } },
        })
        export const voiceCall = {
          subscribe: writable(initial).subscribe,
          refresh() {},
          call(room, name) { calls.push(['call', room, name]) },
        }
        export const clock = () => ''`,
      loader: 'js', resolveDir: resolve(path, '..'),
    }))
    b.onLoad({ filter: /[/\\]api\.ts$/ }, () => ({ contents: "export const api = {}; export const fileURL = () => ''", loader: 'js' }))
  } }],
})
const source = result.outputFiles[0]?.contents
assert.ok(source)
const room = 'room_20261020-103000-0a1b2c3d'
const request = (state: CallState = 'ringing') => ({
  id: 'c7', room, reason: 'CI needs your sign-in', state, askedAt: '2026-10-20T10:00:00Z',
})
const event = (state: CallState = 'ringing') => ({
  kind: 'call-requests', roomId: room, change: { op: 'upsert', id: 'c7', item: request(state) },
})
const turn = async () => { await Promise.resolve(); await Promise.resolve() }

const dir = await mkdtemp(join(process.env.DYNA_SCRATCH ?? tmpdir(), 'call-widget-'))
after(() => rm(dir, { recursive: true, force: true }))
const file = join(dir, 'fixture.mjs')
await writeFile(file, source)
const dom = installDom({ localStorage: undefined })
Object.assign(globalThis, { localStorage: dom.window.localStorage })
const unmounts: (() => Promise<void>)[] = []
after(async () => {
  for (const dispose of unmounts) await dispose()
  dom.window.close()
  for (const name of [...domNames, 'localStorage']) Reflect.deleteProperty(globalThis, name)
})
const { default: Widget, Row, calls, callRequests, mount, unmount, flushSync } = await import(pathToFileURL(file).href)

test('the widget reports Shown once, hides closed toasts, and starts calls only from Accept', async () => {
  const target = document.getElementById('app')
  assert.ok(target)
  const app = mount(Widget, { target, props: {
    rooms: [{ id: room, name: 'voice-test' }], link: { room, call: 'c7' }, onopenfile() {},
  } })
  unmounts.push(() => unmount(app))
  flushSync()
  await turn() // Finish the initial empty snapshot before the live event.
  flushSync()
  app.event({ kind: 'posts', roomId: room, posts: [{ to: ['voice-test:voice-call'], text: 'hello' }] })
  flushSync()
  assert.equal(document.querySelector('.incoming'), null)
  const visibility = (state: DocumentVisibilityState) => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: state })
    document.dispatchEvent(new Event('visibilitychange'))
    flushSync()
  }
  visibility('hidden')
  app.event(event())
  flushSync()
  assert.deepEqual(calls, [], 'a hidden toast must not suppress the fallback push')
  visibility('visible')
  assert.ok(document.querySelector('.incoming.full'), 'a push target gets a full-screen Accept')
  assert.match(document.body.textContent ?? '', /root would like to speak with you.*CI needs your sign-in/)
  assert.deepEqual(calls, [['shown', room, 'c7']], 'display does not start a call')
  app.event(event())
  flushSync()
  visibility('hidden')
  visibility('visible')
  assert.deepEqual(calls, [['shown', room, 'c7']], 'repeat events and visibility changes do not report Shown twice')

  const click = (label: string) => {
    const button = [...document.querySelectorAll('button')].find((b) => b.textContent === label)
    assert.ok(button)
    button.click()
    flushSync()
  }
  click('Accept')
  assert.deepEqual(calls.at(-1), ['call', room, 'voice-test'], 'Accept uses the normal call entry point')
  for (const state of ['joined', 'declined', 'withdrawn'] as const) {
    app.event(event(state))
    flushSync()
    assert.equal(document.querySelector('.incoming'), null, state)
    app.event(event())
    flushSync()
  }
  click('Decline')
  await turn()
  flushSync()
  assert.deepEqual(calls.at(-1), ['decline', room, 'c7'])
  assert.equal(document.querySelector('.incoming'), null)

  const row = mount(Row, { target, props: {
    request: request(), onaccept: () => app.accept(request()), ondecline: () => callRequests.decline(request()),
  } })
  unmounts.push(() => unmount(row))
  flushSync()
  click('Accept')
  assert.deepEqual(calls.at(-1), ['call', room, 'voice-test'], 'the room row uses the same Accept path')
  click('Decline')
  await turn()
  assert.deepEqual(calls.at(-1), ['decline', room, 'c7'])
})
