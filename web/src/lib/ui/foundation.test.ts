import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { need } from '../../../test/need.ts'

// Render the shared SettingRow and StatusPill on the server and read the HTML.
async function load() {
  const dir = await mkdtemp(join(tmpdir(), 'dyna-foundation-ui-'))
  const result = await build({
    bundle: true, write: false,
    stdin: { contents: "export { default as SettingRow } from './SettingRow.svelte'; export { default as StatusPill } from './StatusPill.svelte'; export { render } from 'svelte/server'; export { createRawSnippet } from 'svelte'", resolveDir: new URL('.', import.meta.url).pathname, loader: 'js' },
    format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
    plugins: [svelteEsbuildPlugin('server')],
  })
  const file = join(dir, 'foundation.mjs')
  await writeFile(file, need(result.outputFiles?.[0]).contents)
  return { dir, ...(await import(pathToFileURL(file).href)) }
}

test('a disabled SettingRow dims and makes its control inert; StatusPill colors only a chosen tone', async () => {
  const { dir, SettingRow, StatusPill, render, createRawSnippet } = await load()
  try {
    const control = createRawSnippet(() => ({ render: () => '<button>Switch</button>' }))
    const row = (disabled: boolean): string => render(SettingRow, { props: { title: 'Takes work', control, disabled } }).body
    assert.match(row(true), /class="setting[^"]*disabled[^"]*" aria-disabled="true"[\s\S]*class="control[^"]*" inert/)
    assert.doesNotMatch(row(false), /inert|aria-disabled/)

    const pill = (props: object): string => render(StatusPill, { props: { label: 'State', ...props } }).body
    // r16: the default is a neutral outline with no dot, even when a dot is asked for.
    assert.match(pill({ dot: true }), /class="status pill neutral[^"]*"/)
    assert.doesNotMatch(pill({ dot: true }), /class="mark/)
    assert.match(pill({ tone: 'danger', dot: true }), /class="status pill danger[\s\S]*class="mark/)
    assert.match(pill({ tone: 'live', dot: true }), /class="mark[^"]*pulse/)
    assert.match(pill({ variant: 'dot' }), /class="status dot neutral[\s\S]*class="mark/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})
