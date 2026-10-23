import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'
import { need } from '../../../test/need.ts'

const format = readFileSync(new URL('../ui/format.ts', import.meta.url), 'utf8')
const session = { member: { id: 's1', name: 'lead', address: 'repo:lead', hue: 1, state: 'idle', status: 'idle', turns: 1 }, posts: [], turns: [] }

const code = await bundle({ component: '../sessions/Session', format })

async function render(props: object) {
  const dom = installDom()
  for (const n of ['DOMParser', 'NodeFilter', 'HTMLMediaElement', 'HTMLTextAreaElement']) Reflect.set(globalThis, n, Reflect.get(dom.window, n))
  Object.assign(globalThis, { matchMedia: () => ({ matches: false }) })
  Object.assign(dom.window.HTMLElement.prototype, { scrollTo() {} })
  const Observer = class { observe() {} unobserve() {} disconnect() {} }
  Object.assign(globalThis, { ResizeObserver: Observer })
  Object.assign(dom.window, { ResizeObserver: Observer })
  for (const name of ['offsetHeight', 'clientHeight']) Object.defineProperty(dom.window.HTMLElement.prototype, name, { get() { return 700 } })
  for (const name of ['offsetWidth', 'clientWidth']) Object.defineProperty(dom.window.HTMLElement.prototype, name, { get() { return 1000 } })
  const { dir, default: C, mount, unmount, flushSync } = await importBundle(code, '../sessions/Session')
  let app
  try {
    flushSync(() => { app = mount(C, { target: need(document.getElementById('app'), '#app'), props: { session, room: { id: 'r', name: 'repo' }, members: [], onsend: () => {}, ...props } }) })
    return need(document.getElementById('app'), '#app').innerHTML
  } finally {
    if (app) unmount(app)
    dom.window.close()
    for (const n of [...domNames, 'DOMParser', 'NodeFilter', 'HTMLMediaElement', 'HTMLTextAreaElement']) Reflect.deleteProperty(globalThis, n)
    await rm(dir, { recursive: true, force: true })
  }
}

test('the session view has a composer by default and none when readOnly', async () => {
  assert.match(await render({}), /class="composer[ "]/)
  assert.doesNotMatch(await render({ readOnly: true }), /class="composer[ "]|Message @lead/)
})

test('a read-only transcript cannot remove a queued message', async () => {
  const queue = [{ id: 'q1', from: 'owner', human: true, text: 'wait for me', ts: Date.now(), state: 'waiting', cancellable: true }]
  const props = { queue, onunqueue: () => {}, ondismiss: () => {} }
  assert.match(await render(props), /wait for me[\s\S]*<button[^>]*aria-label="(Dismiss this pending|Remove this waiting)/)
  assert.doesNotMatch(await render({ ...props, readOnly: true }), /aria-label="(Dismiss this pending|Remove this waiting)/)
})
