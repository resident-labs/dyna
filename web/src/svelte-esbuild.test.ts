import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { compileSvelte, svelteEsbuildPlugin } from '../test/svelte-esbuild.ts'

// The fixtures live in a temporary folder, so Svelte's runtime resolves from the web package.
const nodePaths = [fileURLToPath(new URL('../node_modules', import.meta.url))]

test('a typed .svelte.ts module compiles through the plugin', async (t) => {
  const dir = await mkdtemp(join(tmpdir(), 'dyna-svelte-esbuild-'))
  t.after(() => rm(dir, { recursive: true, force: true }))
  await writeFile(join(dir, 'counter.svelte.ts'), `
    export interface Counter { count: number }
    export function createCounter(start: number): Counter {
      const state: Counter = $state({ count: start })
      return state
    }
  `)
  await writeFile(join(dir, 'entry.js'), "export { createCounter } from './counter.svelte.ts'")
  const result = await build({
    entryPoints: [join(dir, 'entry.js')], bundle: true, write: false, format: 'esm', platform: 'node', logLevel: 'silent', nodePaths,
    plugins: [svelteEsbuildPlugin()],
  })
  const out = join(dir, 'out.mjs')
  await writeFile(out, result.outputFiles[0]?.contents ?? '')
  const { createCounter } = await import(pathToFileURL(out).href)
  assert.equal(createCounter(3).count, 3)
})

test('a typed component script compiles through the plugin', async (t) => {
  const dir = await mkdtemp(join(tmpdir(), 'dyna-svelte-esbuild-'))
  t.after(() => rm(dir, { recursive: true, force: true }))
  await writeFile(join(dir, 'Typed.svelte'), '<script lang="ts">\n  let { label }: { label: string } = $props()\n</script>\n<p>{label}</p>\n')
  await writeFile(join(dir, 'entry.js'), "export { default } from './Typed.svelte'")
  const result = await build({
    entryPoints: [join(dir, 'entry.js')], bundle: true, write: false, format: 'esm', platform: 'node', logLevel: 'silent', nodePaths,
    plugins: [svelteEsbuildPlugin()],
  })
  assert.match(result.outputFiles[0]?.text ?? '', /label/)
})

test('client and server output stay separate, including from the cache', () => {
  const source = '<script lang="ts">\n  let { label }: { label: string } = $props()\n</script>\n<p>{label}</p>\n'
  const client = compileSvelte(source, 'Mode.svelte')
  const server = compileSvelte(source, 'Mode.svelte', 'server')
  assert.match(client, /svelte\/internal\/client/)
  assert.match(server, /svelte\/internal\/server/)
  assert.equal(compileSvelte(source, 'Mode.svelte'), client)
  assert.equal(compileSvelte(source, 'Mode.svelte', 'server'), server)
})

test('a typed .svelte.ts module compiles in server mode', async (t) => {
  const dir = await mkdtemp(join(tmpdir(), 'dyna-svelte-esbuild-'))
  t.after(() => rm(dir, { recursive: true, force: true }))
  await writeFile(join(dir, 'counter.svelte.ts'), `
    export function createCounter(start: number): { count: number } {
      const state: { count: number } = $state({ count: start })
      return state
    }
  `)
  await writeFile(join(dir, 'entry.js'), "export { createCounter } from './counter.svelte.ts'")
  const result = await build({
    entryPoints: [join(dir, 'entry.js')], bundle: true, write: false, format: 'esm', platform: 'node', logLevel: 'silent', nodePaths,
    plugins: [svelteEsbuildPlugin('server')],
  })
  assert.doesNotMatch(result.outputFiles[0]?.text ?? '', /svelte\/internal\/client/)
  const out = join(dir, 'out.mjs')
  await writeFile(out, result.outputFiles[0]?.contents ?? '')
  const { createCounter } = await import(pathToFileURL(out).href)
  assert.equal(createCounter(4).count, 4)
})
