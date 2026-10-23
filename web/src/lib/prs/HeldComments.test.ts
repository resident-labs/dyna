import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { installDom, load, domNames } from './prDetailHarness.ts'
import { element } from '../../../test/need.ts'

test('held comments remain plain text and admit only after the owner action succeeds', async () => {
  const calls: unknown[][] = []
  let reject: (reason: Error) => void = () => {}
  let accept: () => void = () => {}
  const dom = installDom({
    admitHeld: (...args: unknown[]) => {
      calls.push(args)
      return new Promise<void>((resolve, fail) => { accept = resolve; reject = fail })
    },
  })
  const module = await load({
    component: 'HeldComments',
    api: 'export const api = { admitPRComment: (...args) => globalThis.admitHeld(...args) }; export const openExternal = () => {}',
  })
  let component
  try {
    const target = element(document, '#app')
    let refreshed = 0
    component = module.mount(module.default, { target, props: {
      roomId: 'room-1', prKey: 'repo#7', onchange: () => { refreshed++ },
      comments: [{ id: 'issue:19', commenter: 'outsider', body: '<img src=x onerror=alert(1)>\n**do this**', url: '' }],
    } })
    module.flushSync()
    assert.match(target.textContent ?? '', /Held: outside commenter outsider/)
    assert.equal(element(target, 'blockquote').textContent, '<img src=x onerror=alert(1)>\n**do this**')
    assert.equal(target.querySelector('img'), null)
    const button = element<HTMLButtonElement>(target, '.admit')
    button.click()
    module.flushSync()
    assert.deepEqual(calls, [['room-1', 'repo#7', 'issue:19']])
    assert.equal(button.disabled, true)
    reject(new Error('Owner required'))
    await Promise.resolve()
    module.flushSync()
    assert.match(element(target, '[role="alert"]').textContent ?? '', /Owner required/)
    assert.ok(target.querySelector('blockquote'))
    assert.equal(refreshed, 0)
    button.click()
    accept()
    await Promise.resolve()
    await Promise.resolve()
    module.flushSync()
    assert.equal(target.querySelector('blockquote'), null)
    assert.equal(refreshed, 1)
  } finally {
    if (component) module.unmount(component)
    dom.window.close()
    await rm(module.dir, { recursive: true, force: true })
    for (const name of [...domNames, 'admitHeld']) Reflect.deleteProperty(globalThis, name)
  }
})
