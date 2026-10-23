import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { bundle, importBundle, installDom, domNames } from '../prs/prDetailHarness.ts'

// The tests read these control properties from the rendered page.
type PageElement = HTMLElement & { value: string; disabled: boolean }
function queryElement(selector: string): PageElement {
  const element = document.querySelector<PageElement>(selector)
  assert.ok(element, selector)
  return element
}
function byId(id: string): PageElement {
  const element = document.getElementById(id)
  assert.ok(element, `#${id}`)
  return element as PageElement
}

const code = await bundle({ component: '../rooms/Rules', format: readFileSync(new URL('../ui/format.ts', import.meta.url), 'utf8'), api: 'export const api = { editRule: (...args) => globalThis.saveRule(...args) }' })
test('inline rules preserve composition, restore focus, and keep failed edits open for both scopes', { timeout: 900 }, async () => {
 const dom = installDom(); globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
 const ui = await importBundle(code, '../rooms/Rules')
 const calls: unknown[][] = []; let fail = false
 Reflect.set(globalThis, 'saveRule', async (...args: unknown[]) => { if (fail) throw Error('Save failed'); calls.push(args) })
 const app = ui.mount(ui.default, { target: document.getElementById('app'), props: { roomId: 'global', view: {
  room: { rules: [{ id: 'r1', text: 'Room rule', setter: 'owner' }] }, global: { rules: [{ id: 'u1', text: 'Global rule', setter: 'owner' }] },
 }, onchanged: async () => {} } })
 const settle = async () => { await Promise.resolve(); ui.flushSync(); await Promise.resolve(); ui.flushSync(); await Promise.resolve(); ui.flushSync() }
 try {
  ui.flushSync()
  for (const id of ['r1', 'u1']) {
   const control = () => byId(`rule-control-${id}`)
   control().click(); await settle()
   const field = () => byId(`rule-edit-${id}`)
   assert.equal(document.activeElement, field())
   const key = (extra: KeyboardEventInit) => field().dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true, ...extra }))
   const before = calls.length
   key({ isComposing: true }); key({ keyCode: 229 }); key({ shiftKey: true }); assert.equal(calls.length, before)
   key({ key: 'Escape' }); await settle(); assert.equal(document.activeElement, control())
   control().click(); await settle(); queryElement('.ghost').click(); await settle(); assert.equal(document.activeElement, control())
   control().click(); await settle(); fail = true; key({}); await settle(); assert.ok(field()); assert.match(document.body.textContent ?? '', /Save failed/)
   const restored = new Promise<void>(resolve => document.addEventListener('focusin', function listen(e) { if (e.target instanceof HTMLElement && e.target.id === `rule-control-${id}`) { document.removeEventListener('focusin', listen); resolve() } }))
   fail = false; key({}); await restored; await settle(); assert.equal(calls.at(-1)?.[1], id === 'u1'); assert.equal(calls.at(-1)?.[2], id)
   assert.equal(document.activeElement, control())
  }
 } finally {
  await ui.unmount(app); dom.window.close()
  for (const name of [...domNames, 'HTMLMediaElement', 'saveRule']) Reflect.deleteProperty(globalThis, name)
  await rm(ui.dir, { recursive: true, force: true })
 }
})

test('a delayed saved-rule refresh does not take focus from a newer edit or another control', async () => {
 for (const scenario of ['new edit', 'outside control']) {
 const dom = installDom(); globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
 const ui = await importBundle(code, '../rooms/Rules')
 Reflect.set(globalThis, 'saveRule', async () => {})
 let resolveRefresh: () => void = () => { throw new Error('no pending refresh') }
 let enteredRefresh: () => void = () => { throw new Error('no pending refresh') }
 const started = new Promise<void>(resolve => { enteredRefresh = resolve })
 const refresh = new Promise<void>(resolve => { resolveRefresh = resolve })
 const app = ui.mount(ui.default, { target: document.getElementById('app'), props: { roomId: 'global', view: {
  room: { rules: [{ id: 'r1', text: 'First rule', setter: 'owner' }, { id: 'r2', text: 'Second rule', setter: 'owner' }] }, global: { rules: [] },
 }, onchanged: () => { enteredRefresh(); return refresh } } })
 const settle = async () => { await Promise.resolve(); ui.flushSync(); await Promise.resolve(); ui.flushSync() }
 try {
  ui.flushSync(); byId('rule-control-r1').click(); await settle()
  byId('rule-edit-r1').dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
  await started; ui.flushSync()
  let target: PageElement
  if (scenario === 'new edit') {
   byId('rule-control-r2').click(); await settle()
   target = byId('rule-edit-r2')
   target.value = 'Keep this draft'; target.dispatchEvent(new Event('input', { bubbles: true })); ui.flushSync()
  } else {
   target = queryElement('[aria-label="Rule text"]'); target.focus()
  }
  resolveRefresh(); await settle(); await settle()
  assert.equal(document.activeElement, target, scenario)
  if (scenario === 'new edit') assert.equal(target.value, 'Keep this draft')
 } finally {
  await ui.unmount(app); dom.window.close()
  for (const name of [...domNames, 'HTMLMediaElement', 'saveRule']) Reflect.deleteProperty(globalThis, name)
  await rm(ui.dir, { recursive: true, force: true })
 }
 }
})
