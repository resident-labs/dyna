import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { load, installDom, domNames } from '../prs/prDetailHarness.ts'
import { element } from '../../../test/need.ts'

type Mounted = object
// The harness is plain JavaScript, so its exports are typed here at the boundary.
type Harness = {
  dir: string
  default: unknown
  mount: (component: unknown, options: { target: Element; props: object }) => Mounted
  unmount: (app: Mounted) => void
  flushSync: (fn?: () => void) => void
}

test('inline comments focus for typing, submit once, and cancel without closing the viewer', async () => {
  const dom = installDom()
  globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
  let api: Harness | undefined, component: Mounted | undefined
  try {
    const app: Harness = await load({ component: '../files/DiffView' })
    api = app
    const target = element(document, '#app'), comments: { text: string }[] = []
    component = app.mount(app.default, { target, props: {
      file: { path: 'a.ts', kind: 'modified', hunks: [{ header: '@@ -1 +1 @@', lines: [{ t: '+', b: 1, text: 'const a = 1' }] }] },
      oncomment: (value: { text: string }) => comments.push(value),
    } })
    app.flushSync()
    const button = element(target, '.addc')
    const open = async (): Promise<HTMLTextAreaElement> => { button.click(); await new Promise<void>(resolve => setImmediate(resolve)); app.flushSync(); return element<HTMLTextAreaElement>(target, 'textarea') }
    const key = (field: HTMLTextAreaElement, name: string, extra: KeyboardEventInit = {}): KeyboardEvent => {
      const event = new KeyboardEvent('keydown', { key: name, bubbles: true, cancelable: true, ...extra })
      field.dispatchEvent(event); app.flushSync(); return event
    }
    const fill = (field: HTMLTextAreaElement, value: string): void => { field.value = value; field.dispatchEvent(new Event('input', { bubbles: true })); app.flushSync() }
    let field = await open()
    assert.equal(document.activeElement, field)
    key(field, 'Enter', { ctrlKey: true })
    assert.equal(comments.length, 0, 'blank drafts cannot submit')
    fill(field, 'Review this line')
    assert.equal(key(field, 'Enter').defaultPrevented, false, 'Enter stays a newline')
    key(field, 'Enter', { ctrlKey: true, isComposing: true })
    assert.equal(comments.length, 0, 'composition must not submit')
    key(field, 'Enter', { ctrlKey: true })
    await new Promise<void>(resolve => setImmediate(resolve))
    assert.equal(comments.length, 1)
    assert.equal(comments[0]?.text, 'Review this line')
    assert.equal(document.activeElement, button)
    field = await open()
    let escaped = 0
    window.addEventListener('keydown', () => escaped++)
    key(field, 'Escape')
    await new Promise<void>(resolve => setImmediate(resolve))
    assert.equal(escaped, 0)
    assert.equal(target.querySelector('textarea'), null)
    assert.equal(document.activeElement, button)
  } finally {
    if (component) await api?.unmount(component)
    if (api) await rm(api.dir, { recursive: true, force: true })
    dom.window.close()
    for (const name of [...domNames, 'HTMLMediaElement']) Reflect.deleteProperty(globalThis, name)
  }
})
