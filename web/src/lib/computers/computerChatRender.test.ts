import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { installDom, importBundle, domNames } from '../prs/prDetailHarness.ts'
import { rm } from 'node:fs/promises'
import { element } from '../../../test/need.ts'

test('ComputerChat uses DmRows and the shared composer, with no renderer of its own', () => {
  const src = readFileSync(new URL('./ComputerChat.svelte', import.meta.url), 'utf8')
  assert.match(src, /import DmRows /)
  assert.match(src, /import ComposerBox /)
  assert.doesNotMatch(src, /actLines|class="act"/)
})

test('computer chat leaves modal Escape to the active dialog', async () => {
  const dom = installDom()
  globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
  // jsdom has no matchMedia; the chat only reads matches and listens for changes.
  Object.defineProperty(globalThis, 'matchMedia', { configurable: true, value: (media: string) => ({ matches: false, media, addEventListener() {}, removeEventListener() {} }) })
  globalThis.addEventListener = dom.window.addEventListener.bind(dom.window)
  globalThis.removeEventListener = dom.window.removeEventListener.bind(dom.window)
  let api: Awaited<ReturnType<typeof importBundle>> | undefined, component: ReturnType<NonNullable<typeof api>['mount']> | undefined
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
    const output = result.outputFiles?.[0]
    assert.ok(output)
    api = await importBundle(output.contents, 'ComputerChat')
    component = api.mount(api.default, { target: document.getElementById('app'), props: { room: { id: 'global', name: 'team' } } })
    api.flushSync()
    const press = (target: Element, key: string): void => {
      target.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
      api?.flushSync()
    }
    press(document.body, '/')
    assert.equal(element(document, '[data-chat]').dataset.state, 'composer')
    const modal = document.createElement('div')
    modal.setAttribute('aria-modal', 'true')
    modal.tabIndex = -1
    document.body.append(modal)
    modal.focus()
    press(modal, 'Escape')
    assert.equal(element(document, '[data-chat]').dataset.state, 'composer')
    modal.remove()
    press(document.body, 'Escape')
    assert.equal(element(document, '[data-chat]').dataset.state, 'tucked')
  } finally {
    if (component) await api?.unmount(component)
    if (api) await rm(api.dir, { recursive: true, force: true })
    dom.window.close()
    for (const name of [...domNames, 'addEventListener', 'removeEventListener', 'HTMLMediaElement', 'matchMedia']) Reflect.deleteProperty(globalThis, name)
  }
})
