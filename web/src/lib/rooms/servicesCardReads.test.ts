import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { readFileSync } from 'node:fs'
import { bundle, importBundle, installDom, domNames } from '../prs/prDetailHarness.ts'

declare global {
  var cardRegistryFails: boolean
  var cardHooksFail: boolean
}
const code = await bundle({
  component: '../rooms/ServicesCard',
  format: readFileSync(new URL('../ui/format.ts', import.meta.url), 'utf8'),
  api: `
    export const api = {
      globalHookSummary: () => globalThis.cardHooksFail
        ? Promise.reject(new Error('hooks unavailable'))
        : Promise.resolve([{ hook: { name: 'custom', label: 'Custom hook' }, lastRun: { id: 'r1', result: 'error', startedAt: '2026-01-01T00:00:00Z' } }]),
      serviceRegistry: () => globalThis.cardRegistryFails
        ? Promise.reject(new Error('registry unavailable'))
        : Promise.resolve([{ id: 'doctor', group: 'this-computer', scope: 'global', label: 'Load doctor', state: 'error', detail: 'last-run-failed', settingsKeys: [] }]),
      globalHookRunLog: () => Promise.resolve({ tail: '' }),
    }
  `,
})

test('a failed read leaves the other source visible', async () => {
  const dom = installDom()
  const ui = await importBundle(code, '../rooms/ServicesCard')
  const target = document.getElementById('app')
  assert.ok(target)
  try {
    for (const registryFails of [true, false]) {
      globalThis.cardRegistryFails = registryFails
      globalThis.cardHooksFail = !registryFails
      const app = ui.mount(ui.default, { target, props: { onsettings() {} } })
      try {
        await Promise.resolve()
        await Promise.resolve()
        ui.flushSync()
        const text = target.textContent ?? ''
        assert.match(text, registryFails ? /Custom hook/ : /Load doctor/)
        assert.match(text, registryFails ? /Could not read services: registry unavailable/ : /Could not read hooks: hooks unavailable/)
      } finally {
        await ui.unmount(app)
      }
    }
  } finally {
    dom.window.close()
    for (const name of domNames) Reflect.deleteProperty(globalThis, name)
    Reflect.deleteProperty(globalThis, 'cardRegistryFails')
    Reflect.deleteProperty(globalThis, 'cardHooksFail')
    await rm(ui.dir, { recursive: true, force: true })
  }
})
