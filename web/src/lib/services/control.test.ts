import assert from 'node:assert/strict'
import test from 'node:test'
import { applyControl, type ControlApi } from './control.ts'

// The page's controls are only names for existing calls, so each id maps to
// exactly one call and one argument pair.
const record = () => {
  const calls: string[] = []
  const api: ControlApi = {
    setReviewEnabled: (room, on) => { calls.push(`review ${room} ${on}`); return Promise.resolve(null) },
    setMergePaused: (room, paused) => { calls.push(`merge-paused ${room} ${paused}`); return Promise.resolve(null) },
    runHook: (repo, name) => { calls.push(`hook-run ${repo} ${name}`); return Promise.resolve({ id: 'run', repo, name, command: '', result: '', logPath: '', startedAt: '' }) },
    editHook: (repo, name, patch) => { calls.push(`hook-edit ${repo} ${name} ${JSON.stringify(patch)}`); return Promise.resolve(null) },
    editService: (name, patch) => { calls.push(`service-edit ${name} ${JSON.stringify(patch)}`); return Promise.resolve(null) },
    resetService: (name) => { calls.push(`service-reset ${name}`); return Promise.resolve(null) },
  }
  return { calls, api }
}

const host = { room: 'room_1', repo: '/repo' }

test('every control on the page writes through the call that owns it', async () => {
  const { calls, api } = record()
  await applyControl(api, host, 'review.enabled', false)
  await applyControl(api, host, 'merge.paused', true)
  await applyControl(api, host, 'update.enabled', false)
  await applyControl(api, host, 'update.run', true)
  await applyControl(api, host, 'service.enabled:doctor', false)
  await applyControl(api, host, 'service.every:maintenance', '30')
  await applyControl(api, host, 'service.timeout:maintenance', '2m0s')
  await applyControl(api, host, 'service.reset:maintenance', true)
  assert.deepEqual(calls, [
    'review room_1 false',
    'merge-paused room_1 true',
    'hook-edit /repo self-update {"disabled":true}',
    'hook-run /repo self-update',
    'service-edit doctor {"disabled":true}',
    'service-edit maintenance {"everyMinutes":30}',
    'service-edit maintenance {"timeout":"2m0s"}',
    'service-reset maintenance',
  ])
})

test('a control the page does not know is an error, never a silent success', async () => {
  const { api } = record()
  await assert.rejects(() => applyControl(api, host, 'nothing.here', true), /unknown control/)
})
