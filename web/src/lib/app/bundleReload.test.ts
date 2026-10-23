import assert from 'node:assert/strict'
import test from 'node:test'
import { bundleReloadSafe, createBundleReload } from './bundleReload.ts'

test('a new bundle reloads once when safe and waits for a draft or voice call', () => {
  let reloads = 0
  const update = createBundleReload(() => reloads++)
  assert.equal(bundleReloadSafe({ drafts: {}, composerPending: false, voicePhase: 'idle', visible: true }), true)
  assert.equal(bundleReloadSafe({ drafts: { room: 'draft' }, composerPending: false, voicePhase: 'idle', visible: true }), false)
  assert.equal(bundleReloadSafe({ drafts: {}, composerPending: true, voicePhase: 'idle', visible: true }), false)
  assert.equal(bundleReloadSafe({ drafts: {}, composerPending: false, voicePhase: 'live', visible: true }), false)
  assert.equal(bundleReloadSafe({ drafts: {}, composerPending: false, voicePhase: 'resumable', visible: true }), false)
  assert.equal(bundleReloadSafe({ drafts: {}, composerPending: false, voicePhase: 'idle', visible: false }), false)

  assert.equal(update.offer('build-b', false), false)
  assert.equal(reloads, 0, 'a draft or call defers the reload')
  assert.equal(update.offer('build-b', false), false, 'another stream event does not duplicate the notice')
  assert.equal(update.retry(true), true, 'the pending update reloads when it becomes safe')
  assert.equal(reloads, 1)
  assert.equal(update.offer('build-c', true), false, 'a later event cannot reload the page twice')
  assert.equal(reloads, 1)
})
