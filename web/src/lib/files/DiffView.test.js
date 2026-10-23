import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { load, installDom, domNames } from '../prs/prDetailHarness.ts'

test('inline comments focus for typing, submit once, and cancel without closing the viewer', async () => {
  const dom = installDom()
  globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
  let api, component
  try {
    api = await load({ component: '../files/DiffView' })
    const target = document.getElementById('app'), comments = []
    component = api.mount(api.default, { target, props: {
      file: { path: 'a.ts', kind: 'modified', hunks: [{ header: '@@ -1 +1 @@', lines: [{ t: '+', b: 1, text: 'const a = 1' }] }] },
      oncomment: value => comments.push(value),
    } })
    api.flushSync()
    const button = target.querySelector('.addc')
    const open = async () => { button.click(); await new Promise(setImmediate); api.flushSync(); return target.querySelector('textarea') }
    const key = (field, name, extra = {}) => {
      const event = new KeyboardEvent('keydown', { key: name, bubbles: true, cancelable: true, ...extra })
      field.dispatchEvent(event); api.flushSync(); return event
    }
    const fill = (field, value) => { field.value = value; field.dispatchEvent(new Event('input', { bubbles: true })); api.flushSync() }
    let field = await open()
    assert.equal(document.activeElement, field)
    key(field, 'Enter', { ctrlKey: true })
    assert.equal(comments.length, 0, 'blank drafts cannot submit')
    fill(field, 'Review this line')
    assert.equal(key(field, 'Enter').defaultPrevented, false, 'Enter stays a newline')
    key(field, 'Enter', { ctrlKey: true, isComposing: true })
    assert.equal(comments.length, 0, 'composition must not submit')
    key(field, 'Enter', { ctrlKey: true })
    await new Promise(setImmediate)
    assert.equal(comments.length, 1)
    assert.equal(comments[0].text, 'Review this line')
    assert.equal(document.activeElement, button)
    field = await open()
    let escaped = 0
    window.addEventListener('keydown', () => escaped++)
    key(field, 'Escape')
    await new Promise(setImmediate)
    assert.equal(escaped, 0)
    assert.equal(target.querySelector('textarea'), null)
    assert.equal(document.activeElement, button)
  } finally {
    if (component) await api.unmount(component)
    if (api) await rm(api.dir, { recursive: true, force: true })
    dom.window.close()
    for (const name of [...domNames, 'HTMLMediaElement']) delete globalThis[name]
  }
})
