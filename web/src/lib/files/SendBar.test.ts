import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { load, installDom, domNames } from '../prs/prDetailHarness.ts'
import { element, elements } from '../../../test/need.ts'

type Mounted = object
// The harness is plain JavaScript, so its exports are typed here at the boundary.
type Harness = {
  dir: string
  default: unknown
  mount: (component: unknown, options: { target: Element; props: object }) => Mounted
  unmount: (app: Mounted) => void
  flushSync: (fn?: () => void) => void
}

// Pending is one inline comment waiting to be sent.
type Pending = { file: string; line: number; quote: string; text: string }

test('pending edits preserve metadata and cannot overlap sending', async () => {
  const dom = installDom()
  globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
  let api: Harness | undefined, instance: Mounted | undefined
  let rejectSend: (reason?: unknown) => void = () => {}
  const delivered: Pending[][] = []
  try {
    const app: Harness = await load({ component: '../files/SendBar' })
    api = app
    const target = element(document, '#app')
    const pending: Pending[] = [{ file: 'a.ts', line: 7, quote: '+old', text: 'First' }, { file: 'b.ts', line: 9, quote: '-old', text: 'Second' }]
    instance = app.mount(app.default, { target, props: { pending, deliver: (batch: Pending[]) => { delivered.push(batch); return new Promise<unknown>((_, reject) => { rejectSend = reject }) } } })
    app.flushSync()
    const edit = element<HTMLButtonElement>(target, 'button.edit')
    assert.ok(edit)
    edit.click(); await new Promise<void>(resolve => setImmediate(resolve)); app.flushSync()
    const field = element<HTMLTextAreaElement>(target, 'textarea')
    assert.equal(document.activeElement, field)
    assert.equal(element<HTMLButtonElement>(target, '.send').disabled, true)
    assert.ok(elements<HTMLButtonElement>(target, '.rm').every(button => button.disabled))
    field.value = 'Updated'; field.dispatchEvent(new Event('input', { bubbles: true })); app.flushSync()
    field.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true, isComposing: true, bubbles: true }))
    app.flushSync()
    assert.ok(target.querySelector('textarea'), 'composition does not save')
    field.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true, bubbles: true }))
    await new Promise<void>(resolve => setImmediate(resolve)); app.flushSync()
    assert.equal(document.activeElement, edit)
    assert.equal(target.querySelector('textarea'), null)
    element<HTMLButtonElement>(target, '.send').click(); app.flushSync()
    assert.deepEqual(delivered, [[{ ...pending[0], text: 'Updated' }, pending[1]]])
    assert.ok(elements<HTMLButtonElement>(target, '.edit, .rm').every(button => button.disabled))
    rejectSend(new Error('offline'))
    await new Promise<void>(resolve => setImmediate(resolve)); app.flushSync()
    assert.match(target.textContent ?? '', /Updated/)
    assert.match(target.textContent ?? '', /offline/)
    assert.equal(element<HTMLButtonElement>(target, '.send').disabled, false)
  } finally {
    if (instance) await api?.unmount(instance)
    if (api) await rm(api.dir, { recursive: true, force: true })
    dom.window.close()
    for (const name of [...domNames, 'HTMLMediaElement']) Reflect.deleteProperty(globalThis, name)
  }
})
