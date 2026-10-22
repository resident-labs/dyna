import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { isRecord } from '../api/guards.ts'
import { domNames, installDom, load } from '../prs/prDetailHarness.ts'
import { call } from '../../../test/svelte-source.ts'

test('the shared phone row keeps input focus and exposes one-shot modifier state', async () => {
  const dom = installDom()
  const loaded: unknown = await load({ component: '../ui/PhoneKeys' })
  if (!isRecord(loaded) || typeof loaded.dir !== 'string' || !('default' in loaded)) assert.fail('PhoneKeys bundle did not load')
  const sent: string[] = []
  let app: unknown
  try {
    const target = document.getElementById('app')
    assert.ok(target)
    call(loaded, 'flushSync', () => {
      app = call(loaded, 'mount', loaded.default, { target, props: { ctrl: true, alt: false, onkey: (key: string) => sent.push(key) } })
    })
    assert.equal(target.querySelectorAll('button').length, 8)
    const ctrl = target.querySelector<HTMLElement>('[aria-label="Ctrl"]')
    assert.ok(ctrl)
    assert.equal(ctrl.getAttribute('aria-pressed'), 'true')
    const e = new dom.window.Event('pointerdown', { cancelable: true, bubbles: true })
    ctrl.dispatchEvent(e)
    assert.equal(e.defaultPrevented, true)
    ctrl.click()
    const tab = target.querySelector<HTMLElement>('[aria-label="Tab"]')
    assert.ok(tab)
    tab.click()
    assert.deepEqual(sent, ['ctrl', 'tab'])
  } finally {
    if (app) call(loaded, 'unmount', app)
    dom.window.close()
    for (const name of domNames) Reflect.deleteProperty(globalThis, name)
    await rm(loaded.dir, { recursive: true, force: true })
  }
})
