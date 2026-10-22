import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'

// The divider renders on the server, so the test reads the real component's
// HTML. Each case is one state the transcript can report.
const nodePaths = [fileURLToPath(new URL('../../../node_modules', import.meta.url))]

interface Renderer {
  default: unknown
  render: (component: unknown, options: { props: Record<string, unknown> }) => { body: string }
}

// load renders the real component from a temporary bundle. The caller keeps
// the folder, so its cleanup removes the bundle too.
async function load(dir: string): Promise<(row: Record<string, unknown>) => string> {
  const result = await build({
    stdin: {
      contents: `export { default } from './CompactionDivider.svelte'; export { render } from 'svelte/server'`,
      resolveDir: fileURLToPath(new URL('.', import.meta.url)),
      loader: 'js',
    },
    bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent', nodePaths,
    plugins: [svelteEsbuildPlugin('server')],
  })
  const file = join(dir, 'CompactionDivider.mjs')
  await writeFile(file, result.outputFiles[0]?.contents ?? new Uint8Array())
  const module: Renderer = await import(pathToFileURL(file).href)
  return (row) => module.render(module.default, { props: { row } }).body.replace(/<!--.*?-->/g, '')
}

test('the divider words each compaction state', async (t) => {
  const dir = await mkdtemp(join(tmpdir(), 'dyna-compaction-'))
  t.after(() => rm(dir, { recursive: true, force: true }))
  const render = await load(dir)
  const ts = Date.UTC(2026, 9, 17, 16, 41)
  const done = render({ state: 'completed', trigger: 'auto', tokensBefore: 182000, tokensAfter: 41000, ts })
  const running = render({ state: 'started', ts })
  const failed = render({ state: 'failed', reason: 'session limit', ts })
  assert.match(done, /Context compacted · 182k → 41k tokens · auto/)
  assert.match(done, /role="separator"/)
  assert.match(running, /Compacting context…/)
  assert.match(running, /class="spin[^"]*"/, 'a running compaction shows its spinner')
  assert.match(failed, /class="compaction[^"]*failed/)
  assert.match(failed, /Context compaction failed: session limit/)
})
