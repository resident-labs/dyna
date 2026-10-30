import test from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { need } from '../../../test/need.ts'
import type { createSettingsPanel } from './settingsPanel.svelte.ts'

const built = await build({
  entryPoints: [fileURLToPath(new URL('./settingsPanel.svelte.ts', import.meta.url))],
  bundle: true, write: false, format: 'esm', platform: 'node',
  conditions: ['browser', 'production'], logLevel: 'silent', plugins: [svelteEsbuildPlugin()],
})
const source = need(built.outputFiles[0]).text
const module: { createSettingsPanel: typeof createSettingsPanel } = await import('data:text/javascript;base64,' + Buffer.from(source).toString('base64'))

test('hooks open on the Services page once per request and require a room', () => {
  let room = true
  let notices = 0
  let services = 0
  const panel = module.createSettingsPanel({ hasRoom: () => room, closeNav() {}, roomRequired: () => { notices++ }, openServices: () => { services++ } })
  const run = { repo: '/one', hook: 'build', runId: 'one' }
  // A getter call, so an assertion does not narrow the next read.
  const at = () => panel.hooksAt
  panel.openSettings('profiles')
  panel.openHooks(run)
  assert.equal(panel.showSettings, false)
  assert.deepEqual(at(), { request: 1, run })
  panel.openHooks()
  assert.deepEqual(at(), { request: 2, run: null })
  assert.equal(services, 2)
  room = false
  panel.openHooks(run)
  assert.equal(at().request, 2)
  assert.equal(services, 2)
  assert.equal(notices, 1)
})
