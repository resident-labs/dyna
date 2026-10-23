// Test helper. Tests that run App.svelte functions in a vm context need every
// app loader (loadPins, loadSync, ...) to exist. This stubs them all from App
// and from the lib/app modules it reads, so adding a loader does not break those
// tests. Override one with Object.assign(c, appLoaderStubs(), { loadX: realOrSpy }).
import { readFileSync, readdirSync } from 'node:fs'

// The stubbed loaders, keyed by the name App.svelte declares them with.
export type AppLoaderStubs = Record<string, () => void>

const LOADER = /^(?: {2})?(?:async )?function (load[A-Z]\w*)\(/gm

// appSources reads App and the lib/app modules that now hold some of its loaders.
function appSources(): string[] {
  const dir = new URL('../../lib/app/', import.meta.url)
  const modules = readdirSync(dir).filter((name) => name.endsWith('.svelte.ts'))
  return [readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8'), ...modules.map((name) => readFileSync(new URL(name, dir), 'utf8'))]
}

export function appLoaderStubs(): AppLoaderStubs {
  const names = appSources().flatMap((source) => [...source.matchAll(LOADER)].map((m) => m[1] ?? ''))
  return Object.fromEntries(names.map((n) => [n, () => {}]))
}
