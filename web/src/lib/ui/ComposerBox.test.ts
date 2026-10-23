import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'
import { element, need } from '../../../test/need.ts'

const code = await bundle({ component: '../ui/ComposerBox', format: `export const short = x => x; export const ago = () => ''; export const markdown = x => x; export const stamp = () => '';` })

test('the toolbar hides Message and keeps real modes and model labels', async () => {
  const dom = installDom()
  Object.assign(globalThis, { HTMLMediaElement: dom.window.HTMLMediaElement, HTMLTextAreaElement: dom.window.HTMLTextAreaElement })
  const ui = await importBundle(code, 'ComposerBox')
  const target = need(document.getElementById('app'))
  try {
    for (const mode of [undefined, 'Message', 'Start session', 'Command', 'Direct message']) {
      const component = ui.mount(ui.default, { target, props: { placeholder: 'Message', model: 'sol', mode, onsend() {} } })
      ui.flushSync()
      try {
        assert.equal(element(target, '.model').textContent, 'sol')
        assert.equal(target.querySelector('.mode')?.textContent ?? null, !mode || mode === 'Message' ? null : mode)
      } finally {
        await ui.unmount(component)
      }
    }
  } finally {
    dom.window.close()
    for (const name of [...domNames, 'HTMLMediaElement', 'HTMLTextAreaElement']) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})
