import assert from 'node:assert/strict'
import test from 'node:test'
import { applyControl, type ControlApi } from './actions.ts'

test('each control id calls the engine call that owns it', async () => {
  const calls: unknown[][] = []
  const rec = (name: string) => (...args: unknown[]) => { calls.push([name, ...args]); return Promise.resolve() }
  const api: ControlApi = { setReviewEnabled: rec('review'), setServiceProfile: rec('profile'), setSetting: rec('setting') }
  await applyControl(api, 'room', 'review.enabled', false)
  await applyControl(api, 'room', 'profile.keeper', 'sonnet')
  await applyControl(api, 'room', 'goals.keeperTimeout', '45m')
  await applyControl(api, 'room', 'worktrees.retention', 'all')
  assert.deepEqual(calls, [
    ['review', 'room', false],
    ['profile', 'room', 'keeper', 'sonnet'],
    ['setting', 'goals.keeperTimeout', '45m'],
    ['setting', 'worktrees.retention', 'all'],
  ])
})
