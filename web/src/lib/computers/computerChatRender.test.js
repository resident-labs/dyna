import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { installDom, importBundle, domNames } from '../prs/prDetailHarness.ts'
import { rm } from 'node:fs/promises'

test('ComputerChat uses DmRows and the shared composer, with no renderer of its own', () => {
  const src = readFileSync(new URL('./ComputerChat.svelte', import.meta.url), 'utf8')
  assert.match(src, /import DmRows /)
  assert.match(src, /import ComposerBox /)
  assert.doesNotMatch(src, /actLines|class="act"/)
})

test('computer chat leaves modal Escape to the active dialog', async () => {
  const dom = installDom()
  globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
  globalThis.addEventListener = dom.window.addEventListener.bind(dom.window)
  globalThis.removeEventListener = dom.window.removeEventListener.bind(dom.window)
  let api, component
  try {
    const result = await build({
      bundle: true, write: false, format: 'esm', platform: 'node',
      conditions: ['browser', 'production'],
      stdin: {
        contents: "export { default } from './ComputerChat.svelte'; export { mount, flushSync, unmount } from 'svelte'",
        resolveDir: new URL('.', import.meta.url).pathname,
      },
      plugins: [{
        name: 'unused-chat-transcript',
        setup(builder) {
          // This test never opens the transcript. Keep the real composer and
          // keyboard handler without compiling the unrelated row renderers.
          builder.onLoad({ filter: /[/\\]DmRows\.svelte$/ }, () => ({
            contents: 'export default function DmRows() {}', loader: 'js',
          }))
        },
      }, svelteEsbuildPlugin()],
    })
    api = await importBundle(result.outputFiles[0].contents, 'ComputerChat')
    component = api.mount(api.default, { target: document.getElementById('app'), props: { room: { id: 'global', name: 'team' } } })
    api.flushSync()
    const press = (target, key) => {
      target.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
      api.flushSync()
    }
    press(document.body, '/')
    assert.equal(document.querySelector('[data-chat]').dataset.state, 'composer')
    const modal = document.createElement('div')
    modal.setAttribute('aria-modal', 'true')
    modal.tabIndex = -1
    document.body.append(modal)
    modal.focus()
    press(modal, 'Escape')
    assert.equal(document.querySelector('[data-chat]').dataset.state, 'composer')
    modal.remove()
    press(document.body, 'Escape')
    assert.equal(document.querySelector('[data-chat]').dataset.state, 'tucked')
  } finally {
    if (component) await api.unmount(component)
    if (api) await rm(api.dir, { recursive: true, force: true })
    dom.window.close()
    for (const name of [...domNames, 'addEventListener', 'removeEventListener', 'HTMLMediaElement']) delete globalThis[name]
  }
})
