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

test('hook logs remount for each request and require a room', () => {
  let room = true
  let notices = 0
  const panel = module.createSettingsPanel({ hasRoom: () => room, closeNav() {}, roomRequired: () => { notices++ } })
  const first = { repo: '/one', hook: 'build', runId: 'one' }
  const second = { repo: '/two', hook: 'test', runId: 'two' }
  panel.openHookLog(first)
  const key = panel.roomSettingsKey
  panel.openHookLog(second)
  assert.equal(panel.roomSettingsKey, key + 1)
  assert.deepEqual(panel.roomSettingsAt.hookRun, second)
  panel.closeRoomSettings()
  room = false
  panel.openHookLog(first)
  assert.equal(panel.showRoomSettings, false)
  assert.equal(panel.roomSettingsKey, key + 1)
  assert.equal(notices, 1)
})
