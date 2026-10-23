import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { bundle, importBundle, installDom, domNames } from '../prs/prDetailHarness.ts'
const code = await bundle({ component: '../rooms/AddBox' })
test('AddBox keeps composition and Shift+Enter separate from submission', async () => {
  const dom = installDom()
  globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
  const ui = await importBundle(code, '../rooms/AddBox')
  let sent = 0
  const component = ui.mount(ui.default, { target: document.getElementById('app'), props: {
    label: 'Rule', submit: 'Add rule', onsubmit() { sent++ },
  } })
  try {
    ui.flushSync()
    const field = document.querySelector('textarea')
    field.value = '日本語'; field.dispatchEvent(new Event('input', { bubbles: true })); ui.flushSync()
    const enter = extra => field.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true, ...extra }))
    for (const extra of [{ isComposing: true }, { keyCode: 229 }, { shiftKey: true }]) enter(extra)
    assert.equal(sent, 0)
    enter({}); assert.equal(sent, 1)
    document.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    assert.equal(sent, 2)
    field.value = ' '; field.dispatchEvent(new Event('input', { bubbles: true })); ui.flushSync()
    enter({}); assert.equal(sent, 2)
  } finally {
    await ui.unmount(component); dom.window.close()
    for (const name of [...domNames, 'HTMLMediaElement']) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})
