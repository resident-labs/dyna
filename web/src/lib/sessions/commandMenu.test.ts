import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'
import { element, need } from '../../../test/need.ts'

const code = await bundle({
  component: '../sessions/Session',
  format: readFileSync(new URL('../ui/format.ts', import.meta.url), 'utf8'),
  api: `export const api = {
    commands: async () => [{ name: 'review', source: 'skill', description: 'Review code', args: [] }],
    runCommand: async (...args) => { globalThis.commandArgs = args; return { message: { id: 'sent' }, deliveries: [], failed: [] } },
  }`,
})
const settle = () => new Promise<void>((resolve) => setImmediate(resolve))

test('the DM menu runs a skill for its session', async () => {
  const dom = installDom()
  for (const n of ['DOMParser', 'NodeFilter', 'HTMLMediaElement', 'HTMLTextAreaElement']) Reflect.set(globalThis, n, Reflect.get(dom.window, n))
  Object.assign(globalThis, { matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }) })
  Object.assign(dom.window.HTMLElement.prototype, { scrollTo() {} })
  const Observer = class { observe() {} unobserve() {} disconnect() {} }
  Object.assign(globalThis, { ResizeObserver: Observer })
  Object.assign(dom.window, { ResizeObserver: Observer })
  const ui = await importBundle(code, '../sessions/Session')
  const target = need(document.getElementById('app'))
  const component = ui.mount(ui.default, { target, props: {
    session: { member: { id: 's1', name: 'lead', address: 'repo:lead', state: 'idle', status: 'idle', turns: 1 }, posts: [], turns: [] },
    room: { id: 'r', name: 'repo' }, members: [], onsend: () => { throw new Error('unexpected message') },
  } })
  try {
    await settle()
    const input = element<HTMLTextAreaElement>(target, 'textarea')
    const type = async (value: string) => {
      input.value = value
      input.setSelectionRange(value.length, value.length)
      input.dispatchEvent(new dom.window.Event('input', { bubbles: true }))
      await settle()
    }
    await type('/')
    assert.match(target.querySelector('[role="option"]')?.textContent ?? '', /\/review/)
    await type('/review check this')
    input.dispatchEvent(new dom.window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await settle()
    assert.deepEqual(Reflect.get(globalThis, 'commandArgs'), ['r', 'review', 'check this', 'repo:lead'])
  } finally {
    await ui.unmount(component)
    dom.window.close()
    for (const n of [...domNames, 'DOMParser', 'NodeFilter', 'HTMLMediaElement', 'HTMLTextAreaElement', 'matchMedia', 'ResizeObserver', 'commandArgs']) Reflect.deleteProperty(globalThis, n)
    await rm(ui.dir, { recursive: true, force: true })
  }
})
