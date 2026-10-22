import { createHash } from 'node:crypto'
import { mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { Plugin } from 'esbuild'
import { compile, compileModule, VERSION } from 'svelte/compiler'
import { stripTypes } from './svelte-source.ts'

// Compiling Svelte sources is most of a component test's time, and many test files compile the
// same components. The output is cached on disk by content, so only the first file to need a
// source pays for it. A rename makes each write atomic for tests running in parallel.
const cacheDir = join(tmpdir(), `dyna-svelte-cache-${VERSION}`)
type Mode = 'client' | 'server'
function cached(kind: string, source: string, filename: string, generate: Mode, compileIt: (source: string, options: { filename: string, generate: Mode }) => { js: { code: string } }): string {
  const file = join(cacheDir, createHash('sha1').update(`${kind}\0${generate}\0${filename}\0${source}`).digest('hex') + '.js')
  try { return readFileSync(file, 'utf8') } catch {}
  const code = compileIt(source, { filename, generate }).js.code
  mkdirSync(cacheDir, { recursive: true })
  const part = `${file}.${process.pid}.part`
  writeFileSync(part, code)
  renameSync(part, file)
  return code
}
export const compileSvelte = (source: string, filename: string, generate: Mode = 'client'): string => cached('component', source, filename, generate, compile)
export const compileSvelteModule = (source: string, filename: string, generate: Mode = 'client'): string => cached('module', source, filename, generate, compileModule)

// Svelte's compileModule does not strip types, so a .svelte.ts module loses them first.
const moduleSource = (path: string): string => {
  const text = readFileSync(path, 'utf8')
  return path.endsWith('.ts') ? stripTypes(text) : text
}

// esbuild plugin that compiles .svelte components and .svelte.js / .svelte.ts modules.
// 'server' output renders to a string; the default 'client' output mounts into a DOM.
export function svelteEsbuildPlugin(generate: Mode = 'client'): Plugin {
  return {
    name: 'svelte-test',
    setup(builder) {
      builder.onLoad({ filter: /\.svelte\.[jt]s$/, namespace: 'file' }, ({ path }) => ({ contents: compileSvelteModule(moduleSource(path), path, generate), loader: 'js', resolveDir: resolve(path, '..') }))
      builder.onLoad({ filter: /\.svelte$/, namespace: 'file' }, ({ path }) => ({ contents: compileSvelte(readFileSync(path, 'utf8'), path, generate), loader: 'js', resolveDir: resolve(path, '..') }))
    },
  }
}
