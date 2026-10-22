import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { appLoaderStubs } from './appStubs.js'

test('appLoaderStubs stubs every load function App.svelte declares, so a new loader needs no test edit', () => {
  const stubs = appLoaderStubs()
  const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
  const declared = [...source.matchAll(/^ {2}(?:async )?function (load[A-Z]\w*)\(/gm)].map((m) => m[1])
  assert.ok(declared.includes('loadSync') && declared.includes('loadIssues'), 'the App loaders are found')
  for (const name of declared) assert.equal(typeof stubs[name], 'function', name)
  assert.equal(stubs.loadSync(), undefined)
})
