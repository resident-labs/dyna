import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { call, runFunctions } from '../../../test/svelte-source.ts'

test('PDF page input accepts only whole pages within the document', () => {
  const source = readFileSync(new URL('./PDFPreview.svelte', import.meta.url), 'utf8')
  const context = {}
  runFunctions(source, ['parsePageInput'], context)
  for (const [input, page] of [['1', 1], [' 40 ', 40], ['80', 80]] as const) {
    assert.equal(call(context, 'parsePageInput', input, 80), page)
  }
  for (const input of ['', ' ', '0', '-1', '1.5', '81', '1e1', 'oops', '999999999999999999999']) {
    assert.equal(call(context, 'parsePageInput', input, 80), null, input)
  }
})

test('Escape resets the page field before the outer dialog can close', async () => {
  const { build } = await import('esbuild')
  const { svelteEsbuildPlugin } = await import('../../../test/svelte-esbuild.ts')
  const { installDom, importBundle, domNames } = await import('../prs/prDetailHarness.ts')
  const { dialog } = await import('../ui/dialog.ts')
  const { rm } = await import('node:fs/promises')
  const dom = installDom()
  globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
  Object.assign(globalThis, { ResizeObserver: class {
    callback: (entries: { contentRect: { width: number } }[]) => void
    constructor(callback: (entries: { contentRect: { width: number } }[]) => void) { this.callback = callback }
    observe() { this.callback([{ contentRect: { width: 600 } }]) }
    disconnect() {}
  } })
  dom.window.HTMLCanvasElement.prototype.getContext = () => null
  let api: Awaited<ReturnType<typeof importBundle>> | undefined
  let instance: ReturnType<NonNullable<typeof api>['mount']> | undefined
  let action: ReturnType<typeof dialog> | undefined
  try {
    const result = await build({
      bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'],
      stdin: { contents: "export { default } from './PDFPreview.svelte'; export { mount, flushSync, unmount } from 'svelte'", resolveDir: new URL('.', import.meta.url).pathname },
      plugins: [svelteEsbuildPlugin(), { name: 'pdf-fixture', setup(builder) {
        builder.onResolve({ filter: /^pdfjs-dist\// }, ({ path }) => ({ path, namespace: 'pdf-fixture' }))
        builder.onLoad({ filter: /worker/, namespace: 'pdf-fixture' }, () => ({ contents: "export default 'worker'" }))
        builder.onLoad({ filter: /pdf\.mjs$/, namespace: 'pdf-fixture' }, () => ({ contents: "export const GlobalWorkerOptions = {}; export const getDocument = () => ({ promise: Promise.resolve({ numPages: 80, getPage: async () => ({ getViewport: () => ({ width: 600, height: 800 }), render: () => ({ promise: Promise.resolve(), cancel() {} }), getTextContent: async () => ({ items: [] }) }) }), destroy() {} })" }))
        builder.onLoad({ filter: /[/\\]api\.ts$/ }, () => ({ contents: "export const fileURL = () => '/fixture'; export const authenticatedFetch = async () => ({ ok: true, arrayBuffer: async () => new ArrayBuffer(0) })" }))
      } }],
    })
    const output = result.outputFiles?.[0]
    assert.ok(output)
    api = await importBundle(output.contents, 'PDFPreview')
    const card = document.createElement('div')
    card.tabIndex = -1
    document.getElementById('app')?.append(card)
    instance = api.mount(api.default, { target: card, props: { roomId: 'global', att: { id: 'fixture', name: 'fixture.pdf' } } })
    api.flushSync()
    await new Promise(setImmediate)
    api.flushSync()
    const field = card.querySelector('input')
    assert.ok(field)
    let closed = 0
    action = dialog(card, { close: () => closed++, initial: () => field })
    await new Promise(setImmediate)
    field.value = '42'
    field.dispatchEvent(new Event('input', { bubbles: true }))
    api.flushSync()
    field.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    api.flushSync()
    assert.equal(closed, 0)
    assert.equal(field.value, '1')
    assert.equal(document.activeElement, field)
  } finally {
    action?.destroy()
    if (instance && api) await api.unmount(instance)
    if (api) await rm(api.dir, { recursive: true, force: true })
    dom.window.close()
    for (const name of [...domNames, 'HTMLMediaElement']) delete (globalThis as Record<string, unknown>)[name]
  }
})
