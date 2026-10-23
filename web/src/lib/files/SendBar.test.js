import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { load, installDom, domNames } from '../prs/prDetailHarness.ts'

test('pending edits preserve metadata and cannot overlap sending', async () => {
  const dom = installDom()
  globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
  let api, instance, rejectSend
  const delivered = []
  try {
    api = await load({ component: '../files/SendBar' })
    const target = document.getElementById('app')
    const pending = [{ file: 'a.ts', line: 7, quote: '+old', text: 'First' }, { file: 'b.ts', line: 9, quote: '-old', text: 'Second' }]
    instance = api.mount(api.default, { target, props: { pending, deliver: (batch) => { delivered.push(batch); return new Promise((_, reject) => { rejectSend = reject }) } } })
    api.flushSync()
    const edit = target.querySelector('button.edit')
    assert.ok(edit)
    edit.click(); await new Promise(setImmediate); api.flushSync()
    const field = target.querySelector('textarea')
    assert.equal(document.activeElement, field)
    assert.equal(target.querySelector('.send').disabled, true)
    assert.ok([...target.querySelectorAll('.rm')].every(button => button.disabled))
    field.value = 'Updated'; field.dispatchEvent(new Event('input', { bubbles: true })); api.flushSync()
    field.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true, isComposing: true, bubbles: true }))
    api.flushSync()
    assert.ok(target.querySelector('textarea'), 'composition does not save')
    field.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true, bubbles: true }))
    await new Promise(setImmediate); api.flushSync()
    assert.equal(document.activeElement, edit)
    assert.equal(target.querySelector('textarea'), null)
    target.querySelector('.send').click(); api.flushSync()
    assert.deepEqual(delivered, [[{ ...pending[0], text: 'Updated' }, pending[1]]])
    assert.ok([...target.querySelectorAll('.edit, .rm')].every(button => button.disabled))
    rejectSend(new Error('offline'))
    await new Promise(setImmediate); api.flushSync()
    assert.match(target.textContent, /Updated/)
    assert.match(target.textContent, /offline/)
    assert.equal(target.querySelector('.send').disabled, false)
  } finally {
    if (instance) await api.unmount(instance)
    if (api) await rm(api.dir, { recursive: true, force: true })
    dom.window.close()
    for (const name of [...domNames, 'HTMLMediaElement']) delete globalThis[name]
  }
})
