import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'
import { element, need } from '../../../test/need.ts'

const code = await bundle({ component: '../rooms/Composer',
  format: `export const short = x => x; export const roomLabel = r => r.name; export const ago = () => ''; export const markdown = x => x; export const stamp = () => '';`,
  api: `export const api = {
    preview: async () => ({mentions: [], wakes: []}),
    listEntries: async (_room, _session, path) => ({entries: path === 'web' ? [{path: 'web/main.ts', name: 'main.ts', kind: 'file'}] : path === 'web docs' ? [{path: 'web docs/odd\`file.txt', name: 'odd\`file.txt', kind: 'file'}] : [{path: 'web', name: 'web', kind: 'dir'}, {path: 'lead', name: 'lead', kind: 'file'}, {path: 'web docs', name: 'web docs', kind: 'dir'}]}),
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

async function withFileComposer(run: (target: Element, input: HTMLTextAreaElement, type: (text: string) => Promise<void>) => Promise<void>) {
  const dom = installDom()
  Object.assign(globalThis, { HTMLMediaElement: dom.window.HTMLMediaElement, HTMLTextAreaElement: dom.window.HTMLTextAreaElement })
  const ui = await importBundle(code, 'Composer')
  const target = need(document.getElementById('app'))
  const component = ui.mount(ui.default, { target, props: { room: { id: 'r', name: 'room' }, choices: ['@lead'], onsend: async () => ({}) } })
  try {
    await settle()
    const input = element<HTMLTextAreaElement>(target, 'textarea')
    await run(target, input, async (text) => {
      input.value = text
      input.setSelectionRange(text.length, text.length)
      input.dispatchEvent(new dom.window.Event('input', { bubbles: true }))
      await settle()
    })
  } finally {
    await ui.unmount(component)
    dom.window.close()
    for (const name of [...domNames, 'HTMLMediaElement', 'HTMLTextAreaElement']) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
}

test('a file and session with the same name keep separate completion targets', async () => {
  await withFileComposer(async (target, input, type) => {
    await type('Review @lead')
    const file = element<HTMLButtonElement>(target, '[role="option"][title="Insert file path"]')
    const session = element<HTMLButtonElement>(target, '[role="option"][title="member"]')
    assert.notEqual(file.id, session.id)
    file.click()
    await settle()
    assert.equal(input.value, 'Review `lead` ')
    await type('Review @lead')
    element<HTMLButtonElement>(target, '[role="option"][title="member"]').click()
    await settle()
    assert.equal(input.value, 'Review @lead ')
  })
})

test('browsing a folder with a space keeps file completion active while typing', async () => {
  await withFileComposer(async (target, input, type) => {
    await type('Review @web')
    const folder = need([...target.querySelectorAll<HTMLButtonElement>('[role="option"]')].find(option => option.textContent?.includes('web docs/')))
    folder.click()
    await settle()
    assert.equal(input.value, 'Review @web docs/')
    assert.match(element(target, '[role="option"]').textContent ?? '', /web docs\/odd`file.txt/)
    await type(input.value + 'odd')
    element<HTMLButtonElement>(target, '[role="option"]').click()
    await settle()
    assert.equal(input.value, 'Review ``web docs/odd`file.txt`` ')
    await type(input.value + '@lead')
    element<HTMLButtonElement>(target, '[role="option"][title="member"]').click()
    await settle()
    assert.equal(input.value, 'Review ``web docs/odd`file.txt`` @lead ')
  })
})
