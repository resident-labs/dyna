import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'

const compiled = await build({
  bundle: true, write: false, platform: 'node', format: 'esm', logLevel: 'silent', loader: { '.svg': 'dataurl', '.png': 'dataurl' },
  stdin: { contents: "export { default as Home } from './Home.svelte'; export { render } from 'svelte/server'", resolveDir: new URL('.', import.meta.url).pathname },
  plugins: [svelteEsbuildPlugin('server')],
})
const output = compiled.outputFiles?.[0]
assert.ok(output)
const { Home, render } = await import('data:text/javascript;base64,' + Buffer.from(output.contents).toString('base64'))

test('the loaded zero-room home offers the New room flow instead of All clear', () => {
  const html = render(Home, { props: {
    rooms: [], roomsLoaded: true, onroom() {}, onaccount() {}, onsettings() {}, onnewroom() {},
  } }).body
  assert.match(html, /Create your first room/)
  assert.match(html, /<button[^>]*>New room<\/button>/)
  assert.doesNotMatch(html, /All clear/)
})
