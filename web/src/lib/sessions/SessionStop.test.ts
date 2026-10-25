import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'
import { need } from '../../../test/need.ts'

const format = readFileSync(new URL('../ui/format.ts', import.meta.url), 'utf8')
const session = { member: { id: 's1', name: 'lead', address: 'repo:lead', hue: 1, state: 'live', status: 'working', turns: 1 }, posts: [], turns: [] }
const code = await bundle({ component: '../sessions/Session', format })

test('the live status Stop action requests a stop for this session', async () => {
  const dom = installDom()
  for (const n of ['DOMParser', 'NodeFilter', 'HTMLMediaElement', 'HTMLTextAreaElement', 'matchMedia']) Reflect.set(globalThis, n, Reflect.get(dom.window, n))
  Object.assign(globalThis, { matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }) })
  Object.assign(dom.window.HTMLElement.prototype, { scrollTo() {} })
  const Observer = class { observe() {} unobserve() {} disconnect() {} }
  Object.assign(globalThis, { ResizeObserver: Observer })
  Object.assign(dom.window, { ResizeObserver: Observer })
  for (const name of ['offsetHeight', 'clientHeight']) Object.defineProperty(dom.window.HTMLElement.prototype, name, { get() { return 700 } })
  for (const name of ['offsetWidth', 'clientWidth']) Object.defineProperty(dom.window.HTMLElement.prototype, name, { get() { return 1000 } })
  const { dir, default: C, mount, unmount, flushSync } = await importBundle(code, '../sessions/Session')
  let app
  try {
    const calls: string[] = []
    flushSync(() => {
      app = mount(C, { target: need(document.getElementById('app'), '#app'), props: {
        session, room: { id: 'r', name: 'repo' }, members: [], onsend: () => {}, onstop: (member: { id: string }) => calls.push(member.id),
      } })
    })
    const button = document.querySelector<HTMLButtonElement>('.working button')
    assert.ok(button)
    assert.equal(button.textContent, 'Stop')
    button.click()
    assert.deepEqual(calls, ['s1'])
  } finally {
    if (app) unmount(app)
    dom.window.close()
    for (const n of [...domNames, 'DOMParser', 'NodeFilter', 'HTMLMediaElement', 'HTMLTextAreaElement', 'matchMedia']) Reflect.deleteProperty(globalThis, n)
    await rm(dir, { recursive: true, force: true })
  }
})
