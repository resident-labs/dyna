import assert from 'node:assert/strict'
import test from 'node:test'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'
import { need } from '../../../test/need.ts'

const code = await bundle({
  component: '../../../test/ComposerPendingHarness',
  format: `export const short = x => x; export const roomLabel = r => r.name; export const ago = () => ''; export const markdown = x => x; export const stamp = () => '';`,
  api: `export const api = { preview: async () => ({mentions: [], wakes: []}), listEntries: async () => ({entries: []}) }`,
})

test('a reactive parent tracks composer pending work without an effect loop', async () => {
  const dom = installDom()
  Object.assign(globalThis, { HTMLMediaElement: dom.window.HTMLMediaElement, HTMLTextAreaElement: dom.window.HTMLTextAreaElement })
  const ui = await importBundle(code, 'ComposerPendingHarness')
  const target = need(document.getElementById('app'))
  let component: ReturnType<typeof ui.mount> | undefined
  try {
    component = ui.mount(ui.default, { target })
    ui.flushSync()
    assert.equal(target.querySelector('[aria-label="Composer pending"]')?.textContent, 'false')
  } finally {
    if (component) await ui.unmount(component)
    dom.window.close()
    for (const name of [...domNames, 'HTMLMediaElement', 'HTMLTextAreaElement']) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})
