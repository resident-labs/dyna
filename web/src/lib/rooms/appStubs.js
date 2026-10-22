// Test helper. Tests that run App.svelte functions in a vm context need every
// app loader (loadPins, loadSync, ...) to exist. This stubs them all from the
// App source, so adding a loader does not break those tests. Override one with
// Object.assign(c, appLoaderStubs(), { loadX: realOrSpy }).
import { readFileSync } from 'node:fs'

export function appLoaderStubs() {
  const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
  const names = [...source.matchAll(/^ {2}(?:async )?function (load[A-Z]\w*)\(/gm)].map((m) => m[1])
  return Object.fromEntries(names.map((n) => [n, () => {}]))
}
