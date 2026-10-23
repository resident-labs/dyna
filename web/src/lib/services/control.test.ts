import assert from 'node:assert/strict'
import test from 'node:test'
import { applyControl, type ControlApi } from './control.ts'

// The page's controls are only names for existing calls, so each id maps to
// exactly one call and one argument pair.
const record = () => {
  const calls: string[] = []
  const api: ControlApi = {
    setReviewEnabled: (room, on) => { calls.push(`review ${room} ${on}`); return Promise.resolve(null) },
    setServiceProfile: (room, role, profile) => { calls.push(`service-profile ${room} ${role} ${profile}`); return Promise.resolve(null) },
    setSetting: (key, value) => { calls.push(`setting ${key} ${value}`); return Promise.resolve({ key, kind: 'string', value, source: 'file', help: '', liveVsRestart: '' }) },
    runHook: (repo, name) => { calls.push(`hook-run ${repo} ${name}`); return Promise.resolve({ id: 'run_1', repo, name, command: '', result: 'pass', logPath: '', startedAt: '2026-10-23T12:00:00Z' }) },
  }
  return { calls, api }
}

const host = { room: 'room_1', repo: '/repo' }

test('every control on the page writes through the call that owns it', async () => {
  const { calls, api } = record()
  await applyControl(api, host, 'review.enabled', false)
  await applyControl(api, host, 'review.profile', 'sol')
  await applyControl(api, host, 'steward.profile', 'off')
  await applyControl(api, host, 'library.profile', 'sonnet')
  await applyControl(api, host, 'update.run', true)
  assert.deepEqual(calls, [
    'review room_1 false',
    'service-profile room_1 reviewer sol',
    'service-profile room_1 merge-steward off',
    'setting library.profile sonnet',
    'hook-run /repo self-update',
  ])
})

test('a control the page does not know is an error, never a silent success', async () => {
  const { api } = record()
  await assert.rejects(() => applyControl(api, host, 'nothing.here', true), /unknown control/)
})
