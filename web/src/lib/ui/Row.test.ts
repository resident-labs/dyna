import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { isRecord } from '../api/guards.ts'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { renderedBody } from '../../../test/svelte-render.ts'
import { call } from '../../../test/svelte-source.ts'

// Compile and server-render the real Row through the shared Svelte build.
const dir = await mkdtemp(join(tmpdir(), 'dyna-row-test-'))
const result = await build({
  bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
  stdin: { contents: "export { default } from './Row.svelte'; export { render } from 'svelte/server'", resolveDir: new URL('.', import.meta.url).pathname, loader: 'js' },
  plugins: [svelteEsbuildPlugin('server')],
})
const bundled = result.outputFiles?.[0]
assert.ok(bundled !== undefined, 'Row did not compile')
await writeFile(join(dir, 'Row.mjs'), bundled.contents)
const loaded: unknown = await import(pathToFileURL(join(dir, 'Row.mjs')).href)
if (!isRecord(loaded) || !('default' in loaded)) assert.fail('Row bundle has no component')
const Row = loaded.default
test.after(() => rm(dir, { recursive: true, force: true }))

// A selectable row is a toggle; an ordinary action row is a plain button.
test('a clickable row exposes aria-pressed only when it is selectable', () => {
  const button = (props: { selected?: boolean }): string => {
    const tag = renderedBody(call(loaded, 'render', Row, { props: { text: 'PR', onclick: () => {}, ...props } })).match(/<button[^>]*>/)?.[0]
    assert.ok(tag !== undefined)
    return tag
  }
  assert.match(button({ selected: true }), /aria-pressed="true"/)
  assert.match(button({ selected: false }), /aria-pressed="false"/)
  assert.doesNotMatch(button({}), /aria-pressed/)
})
