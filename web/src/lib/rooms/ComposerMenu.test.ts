import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'
import { element, need } from '../../../test/need.ts'

const code = await bundle({ component: '../rooms/Composer',
  format: `export const short = x => x; export const roomLabel = r => r.name; export const ago = () => ''; export const markdown = x => x; export const stamp = () => '';`,
  api: `export const api = {
    preview: async () => ({mentions: [], wakes: []}),
    commands: async () => globalThis.loadCommands ? globalThis.loadCommands() : [{ name: 'review', source: 'skill', description: 'Review code', args: [] }],
    runCommand: async (...args) => { globalThis.commandArgs = args; return { message: { id: 'sent' }, deliveries: [], failed: [{ to: 'lead', error: 'unavailable' }] } },
    listEntries: async () => ({entries: []}),
  }`,
})
const settle = () => new Promise<void>(resolve => setImmediate(resolve))

// Two profiles on one model: only the tier in the hint tells them apart.
const profiles = [
  { name: 'sol', harness: 'pi', model: 'gpt-6.1-sol' },
  { name: 'sol-ultrafast', harness: 'pi', model: 'gpt-6.1-sol', default: true },
]

test('the slash menu names each profile and explains an empty result', async () => {
  const dom = installDom()
  Object.assign(globalThis, { HTMLMediaElement: dom.window.HTMLMediaElement, HTMLTextAreaElement: dom.window.HTMLTextAreaElement })
  const ui = await importBundle(code, 'Composer')
  const target = need(document.getElementById('app'))
  const component = ui.mount(ui.default, { target, props: { room: { id: 'r', name: 'room' }, choices: ['@root'], profiles, prepareText: (text: string) => '@lead ' + text, onsend: async () => ({}) } })
  try {
    await settle()
    const input = element<HTMLTextAreaElement>(target, 'textarea')
    const type = async (text: string) => {
      input.value = text
      input.setSelectionRange(text.length, text.length)
      input.dispatchEvent(new dom.window.Event('input', { bubbles: true }))
      await settle()
    }
    await type('/')
    const hints = [...target.querySelectorAll('[role="option"]')].map((row) => row.getAttribute('title'))
    assert.deepEqual(hints, ['Pi · GPT 6.1 Sol', 'Pi · GPT 6.1 Sol · Ultrafast · default', 'Review code'])
    await type('/review check this')
    input.dispatchEvent(new dom.window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await settle()
    assert.deepEqual(Reflect.get(globalThis, 'commandArgs'), ['r', 'review', '@lead check this'])
    assert.match(target.textContent ?? '', /lead: unavailable/)
    await type('/zz')
    assert.equal(target.querySelector('.pop.status')?.textContent, 'No session or command matches “/zz”.')
  } finally {
    await ui.unmount(component)
    dom.window.close()
    for (const name of [...domNames, 'HTMLMediaElement', 'HTMLTextAreaElement']) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})

test('sending waits for the command list', async () => {
  const dom = installDom()
  Object.assign(globalThis, { HTMLMediaElement: dom.window.HTMLMediaElement, HTMLTextAreaElement: dom.window.HTMLTextAreaElement })
  let resolve: (value: unknown[]) => void = () => {}
  Reflect.set(globalThis, 'loadCommands', () => new Promise<unknown[]>((done) => { resolve = done }))
  Reflect.deleteProperty(globalThis, 'commandArgs')
  const ui = await importBundle(code, 'Composer')
  const target = need(document.getElementById('app'))
  let normalSends = 0
  const component = ui.mount(ui.default, { target, props: { room: { id: 'r', name: 'room' }, profiles: [], onsend: async () => { normalSends++; return {} } } })
  try {
    await settle()
    const input = element<HTMLTextAreaElement>(target, 'textarea')
    input.value = '/review check'
    input.setSelectionRange(input.value.length, input.value.length)
    input.dispatchEvent(new dom.window.Event('input', { bubbles: true }))
    input.dispatchEvent(new dom.window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await settle()
    assert.equal(normalSends, 0)
    assert.equal(Reflect.get(globalThis, 'commandArgs'), undefined)
    resolve([{ name: 'review', source: 'skill', description: 'Review code', args: [] }])
    await settle()
    assert.deepEqual(Reflect.get(globalThis, 'commandArgs'), ['r', 'review', 'check'])
  } finally {
    await ui.unmount(component)
    dom.window.close()
    Reflect.deleteProperty(globalThis, 'loadCommands')
    for (const name of [...domNames, 'HTMLMediaElement', 'HTMLTextAreaElement']) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})
