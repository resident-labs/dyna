import { readFileSync } from 'node:fs'
import { mkdtemp, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { basename, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { JSDOM } from 'jsdom'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'

export const domNames = ['window', 'document', 'Element', 'Node', 'HTMLElement', 'Event', 'MouseEvent', 'DocumentFragment', 'ShadowRoot', 'CharacterData', 'Text', 'Comment', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLAnchorElement', 'HTMLDivElement', 'KeyboardEvent', 'CustomEvent', 'IntersectionObserver', 'ResizeObserver', 'HTMLTemplateElement', 'MutationObserver', 'getComputedStyle', 'requestAnimationFrame']

// A jsdom page whose window fills the globals a compiled Svelte component needs.
export function installDom(extra: object = {}): JSDOM {
  const dom = new JSDOM('<!doctype html><div id="app"></div>', { pretendToBeVisual: true, url: 'http://localhost/' })
  Object.assign(globalThis, Object.fromEntries(domNames.map((n) => [n, n === 'window' ? dom.window : Reflect.get(dom.window, n)])), extra)
  return dom
}

const DETAIL_API = 'export const api = { githubDiff: (...a) => { globalThis.diffCalls.push(a); return Promise.resolve({ text: globalThis.diffText }) }, changeDetail: () => new Promise(() => {}), roomChangePolicy: () => new Promise(() => {}) }; export const openExternal = () => {}'

export const FORMAT = "export const markdown = (t) => t || ''; export const ago = () => 'now'; export const initials = (n) => n.slice(0, 2); export const short = (n) => n;"

// A browser component with API overrides and all other real API exports.
export async function load(opts: BundleOptions = {}) {
  return importBundle(await bundle(opts), opts.component)
}

// importBundle imports built code as a fresh module, with its own Svelte
// runtime. A file that mounts one component many times builds it once with
// bundle and imports it per test, in that test's own DOM.
export async function importBundle(code: Uint8Array | string, component = 'PRDetail') {
  const dir = await mkdtemp(join(tmpdir(), 'dyna-pr-detail-'))
  const file = join(dir, `${basename(component)}.mjs`)
  await writeFile(file, code)
  return { dir, ...(await import(pathToFileURL(file).href)) }
}

// bundle builds the component and returns its code.
export interface BundleOptions {
  component?: string
  api?: string
  format?: string
}
export async function bundle({ component = 'PRDetail', api = DETAIL_API, format = FORMAT }: BundleOptions = {}): Promise<Uint8Array> {
  const result = await build({
    bundle: true, write: false, loader: { '.svg': 'dataurl', '.png': 'dataurl' },
    stdin: { contents: `export { default } from './${component}.svelte'; export { mount, flushSync, unmount } from 'svelte'`, resolveDir: new URL('.', import.meta.url).pathname, loader: 'js' },
    format: 'esm', platform: 'node', conditions: ['browser', 'production'], logLevel: 'silent',
    plugins: [svelteEsbuildPlugin(), { name: 'pr-detail-stubs', setup(builder) {
      builder.onLoad({ filter: /[/\\]format\.js$/ }, () => ({ contents: format, loader: 'js' }))
      // Keep new production exports available when a fixture replaces only a few.
      builder.onResolve({ filter: /^actual-api:/ }, ({ path }) => ({ path: path.slice('actual-api:'.length), namespace: 'actual-api' }))
      builder.onLoad({ filter: /.*/, namespace: 'actual-api' }, ({ path }) => ({ contents: readFileSync(path, 'utf8'), loader: 'js', resolveDir: resolve(path, '..') }))
      builder.onLoad({ filter: /[/\\]api\.js$/, namespace: 'file' }, ({ path }) => ({ contents: `export * from ${JSON.stringify('actual-api:' + path)};\n${api}`, loader: 'js', resolveDir: resolve(path, '..') }))
    } }],
  })
  const output = result.outputFiles?.[0]
  if (!output) throw new Error('the bundle has no output')
  return output.contents
}
