import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { mkdtemp, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { compile } from 'svelte/compiler'
import { build } from 'esbuild'
import { need } from '../../../test/need.ts'

// Render the real PRChecks on the server and read the text.
async function load() {
  const dir = await mkdtemp(join(process.env.DYNA_SCRATCH ?? tmpdir(), 'dyna-checks-'))
  const result = await build({
    bundle: true, write: false,
    stdin: { contents: `export { default } from './PRChecks.svelte'; export { render } from 'svelte/server'`, resolveDir: new URL('.', import.meta.url).pathname, loader: 'js' },
    format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
    plugins: [{ name: 'svelte-test', setup(builder) {
      builder.onLoad({ filter: /\.svelte$/ }, async ({ path }) => ({ contents: compile(readFileSync(path, 'utf8'), { filename: path, generate: 'server' }).js.code, loader: 'js', resolveDir: resolve(path, '..') }))
      builder.onLoad({ filter: /[/\\]api\.js$/ }, () => ({ contents: 'export const openExternal = () => { throw new Error("unexpected browser request") }', loader: 'js' }))
    } }],
  })
  const file = join(dir, 'PRChecks.mjs')
  await writeFile(file, need(result.outputFiles?.[0]).contents)
  const module = await import(pathToFileURL(file).href)
  return (props: object): string => module.render(module.default, { props }).body
}

test('Checks renders saved failure output as plain text', async () => {
  const render = await load()
  const html = render({ checks: [
    { name: 'unit', state: 'failure', failureExcerpt: 'FAIL broken\nexpected <green> & got red', url: 'https://github.com/owner/repo/actions/runs/81/job/91' },
    { name: 'CI / lint', state: 'success' },
  ] }).replace(/<!--.*?-->/g, '')
  assert.match(html, /FAIL broken/)
  assert.match(html, /expected &lt;green> &amp; got red/)
  assert.match(html, /<pre[^>]*role="region"[^>]*aria-label="Saved failure output: unit"/)
  assert.match(html, /Open the full log/)
})

test('main CI without a head shows its reason without a revision phrase', async () => {
  const render = await load()
  for (const state of ['pending', 'failed']) {
    const html = render({ main: { state, head: '', reason: 'waiting for saved main CI evidence.' } })
    assert.match(html, /Waiting for saved main CI evidence\./)
    assert.doesNotMatch(html, /(?:running|failed) at/)
  }
})

test('paused room merging has a warning row even when PR checks passed', async () => {
  const render = await load()
  const html = render({
    checks: [{ name: 'CI / unit', state: 'success' }],
    main: { state: 'paused', head: '', reason: 'merging paused in this room' },
  })
  assert.match(html, /Merging paused in this room/)
  assert.match(html, /class="cdot gold/)
  assert.doesNotMatch(html, /class="cdot run/)
})
