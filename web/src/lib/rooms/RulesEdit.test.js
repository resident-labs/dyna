import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { bundle, importBundle, installDom, domNames } from '../prs/prDetailHarness.ts'
const code = await bundle({ component: '../rooms/Rules', format: readFileSync(new URL('../ui/format.ts', import.meta.url), 'utf8'), api: 'export const api = { editRule: (...args) => globalThis.saveRule(...args) }' })
test('inline rules preserve composition, restore focus, and keep failed edits open for both scopes', { timeout: 900 }, async () => {
 const dom = installDom(); globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
 const ui = await importBundle(code, '../rooms/Rules')
 const calls = []; let fail = false
 globalThis.saveRule = async (...args) => { if (fail) throw Error('Save failed'); calls.push(args) }
 const app = ui.mount(ui.default, { target: document.getElementById('app'), props: { roomId: 'global', view: {
  room: { rules: [{ id: 'r1', text: 'Room rule', setter: 'owner' }] }, global: { rules: [{ id: 'u1', text: 'Global rule', setter: 'owner' }] },
 }, onchanged: async () => {} } })
 const settle = async () => { await Promise.resolve(); ui.flushSync(); await Promise.resolve(); ui.flushSync(); await Promise.resolve(); ui.flushSync() }
 try {
  ui.flushSync()
  for (const id of ['r1', 'u1']) {
   const control = () => document.getElementById(`rule-control-${id}`)
   control().click(); await settle()
   const field = () => document.getElementById(`rule-edit-${id}`)
   assert.equal(document.activeElement, field())
   const key = extra => field().dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true, ...extra }))
   const before = calls.length
   key({ isComposing: true }); key({ keyCode: 229 }); key({ shiftKey: true }); assert.equal(calls.length, before)
   key({ key: 'Escape' }); await settle(); assert.equal(document.activeElement, control())
   control().click(); await settle(); document.querySelector('.ghost').click(); await settle(); assert.equal(document.activeElement, control())
   control().click(); await settle(); fail = true; key({}); await settle(); assert.ok(field()); assert.match(document.body.textContent, /Save failed/)
   const restored = new Promise(resolve => document.addEventListener('focusin', function listen(e) { if (e.target.id === `rule-control-${id}`) { document.removeEventListener('focusin', listen); resolve() } }))
   fail = false; key({}); await restored; await settle(); assert.equal(calls.at(-1)[1], id === 'u1'); assert.equal(calls.at(-1)[2], id)
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
 globalThis.saveRule = async () => {}
 let resolveRefresh, enteredRefresh
 const started = new Promise(resolve => { enteredRefresh = resolve })
 const refresh = new Promise(resolve => { resolveRefresh = resolve })
 const app = ui.mount(ui.default, { target: document.getElementById('app'), props: { roomId: 'global', view: {
  room: { rules: [{ id: 'r1', text: 'First rule', setter: 'owner' }, { id: 'r2', text: 'Second rule', setter: 'owner' }] }, global: { rules: [] },
 }, onchanged: () => { enteredRefresh(); return refresh } } })
 const settle = async () => { await Promise.resolve(); ui.flushSync(); await Promise.resolve(); ui.flushSync() }
 try {
  ui.flushSync(); document.getElementById('rule-control-r1').click(); await settle()
  document.getElementById('rule-edit-r1').dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
  await started; ui.flushSync()
  let target
  if (scenario === 'new edit') {
   document.getElementById('rule-control-r2').click(); await settle()
   target = document.getElementById('rule-edit-r2')
   target.value = 'Keep this draft'; target.dispatchEvent(new Event('input', { bubbles: true })); ui.flushSync()
  } else {
   target = document.querySelector('[aria-label="Rule text"]'); target.focus()
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
