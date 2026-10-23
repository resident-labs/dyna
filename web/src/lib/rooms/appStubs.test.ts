import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { appLoaderStubs } from './appStubs.ts'

// The loaders a sliced App function may call now live in App and in the lib/app
// modules that hold the lists, so the stubs come from both.
function declared(): string[] {
  const dir = new URL('../../lib/app/', import.meta.url)
  const sources = [readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')]
  for (const name of readdirSync(dir)) {
    if (name.endsWith('.svelte.ts')) sources.push(readFileSync(new URL(name, dir), 'utf8'))
  }
  return sources.flatMap((source) => [...source.matchAll(/^(?: {2})?(?:async )?function (load[A-Z]\w*)\(/gm)].map((m) => m[1] ?? ''))
}

test('appLoaderStubs stubs every load function App and its modules declare, so a new loader needs no test edit', () => {
  const stubs = appLoaderStubs()
  const names = declared()
  assert.ok(names.includes('loadSync') && names.includes('loadIssues'), 'the loaders are found')
  for (const name of names) assert.equal(typeof stubs[name], 'function', name)
  assert.equal(stubs['loadSync']?.(), undefined)
})
