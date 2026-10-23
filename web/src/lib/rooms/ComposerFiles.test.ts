import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'
import { element, need } from '../../../test/need.ts'

const code = await bundle({ component: '../rooms/Composer',
  format: `export const short = x => x; export const roomLabel = r => r.name; export const ago = () => ''; export const markdown = x => x; export const stamp = () => '';`,
  api: `export const api = {
    preview: async () => ({mentions: [], wakes: []}),
    listEntries: async (_room, _session, path) => ({entries: path === 'web' ? [{path: 'web/main.ts', name: 'main.ts', kind: 'file'}] : [{path: 'web', name: 'web', kind: 'dir'}]}),
  }`,
})
const settle = () => new Promise<void>(resolve => setImmediate(resolve))

test('file choices browse folders, keep paths out of recipients, and preserve text when choosing a profile', async () => {
  const dom = installDom()
  Object.assign(globalThis, { HTMLMediaElement: dom.window.HTMLMediaElement, HTMLTextAreaElement: dom.window.HTMLTextAreaElement })
  const ui = await importBundle(code, 'Composer')
  const target = need(document.getElementById('app'))
  const sent: unknown[][] = []
  const component = ui.mount(ui.default, { target, props: {
    room: { id: 'r', name: 'room' }, choices: ['@root'], profiles: [{ name: 'sol', model: 'gpt-6.1-sol' }],
    onsend: async (...args: unknown[]) => { sent.push(args); return {} },
  } })
  try {
    await settle()
    const input = element<HTMLTextAreaElement>(target, 'textarea')
    input.value = 'Review @web'
    input.setSelectionRange(input.value.length, input.value.length)
    input.dispatchEvent(new dom.window.Event('input', { bubbles: true }))
    await settle()
    element<HTMLButtonElement>(target, '[role="option"]').click()
    await settle()
    assert.equal(input.value, 'Review @web/')
    const file = element<HTMLButtonElement>(target, '[role="option"]')
    assert.match(file.textContent ?? '', /web\/main.ts/)
    assert.equal(input.getAttribute('aria-activedescendant'), file.id)
    input.dispatchEvent(new dom.window.KeyboardEvent('keydown', {key: 'Enter', bubbles: true}))
    await settle()
    assert.equal(input.value, 'Review `web/main.ts` ')
    const profile = element<HTMLSelectElement>(target, '[aria-label="Session profile"]')
    profile.value = 'sol'
    profile.dispatchEvent(new dom.window.Event('change', { bubbles: true }))
    await settle()
    assert.equal(input.value, '/sol Review `web/main.ts` ')
    assert.match(target.textContent ?? '', /Start session/)
    profile.value = ''
    profile.dispatchEvent(new dom.window.Event('change', { bubbles: true }))
    await settle()
    assert.equal(input.value, 'Review `web/main.ts` ')
    input.dispatchEvent(new dom.window.KeyboardEvent('keydown', {key: 'Enter', isComposing: true, bubbles: true}))
    await settle()
    assert.equal(sent.length, 0)
    element<HTMLButtonElement>(target, '[aria-label="Send"]').click()
    await settle()
    assert.equal(sent.length, 1)
    assert.equal(sent[0]?.[0], 'Review `web/main.ts` ')
  } finally {
    await ui.unmount(component)
    dom.window.close()
    for (const name of [...domNames, 'HTMLMediaElement', 'HTMLTextAreaElement']) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})
