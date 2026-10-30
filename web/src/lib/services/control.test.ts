import assert from 'node:assert/strict'
import test from 'node:test'
import { switchWrite, type ControlApi } from './control.ts'
import type { RegistryService } from '../api/service.ts'

const service = (id: RegistryService['id']): RegistryService =>
  ({ id, scope: 'global', group: 'this-computer', label: id, description: '', state: 'on', detail: '', count: 0, profile: '', session: '', settingsKeys: [] })

test('each switch writes through the call that owns it, and the librarian has none', async () => {
  const calls: string[] = []
  const api: ControlApi = {
    setReviewEnabled: (room, on) => { calls.push(`review ${room} ${on}`); return Promise.resolve(null) },
    setMergePaused: (room, paused) => { calls.push(`merge-paused ${room} ${paused}`); return Promise.resolve(null) },
    editService: (name, patch) => { calls.push(`service-edit ${name} ${JSON.stringify(patch)}`); return Promise.resolve(null) },
  }
  const host = { room: 'room_1', repo: '/repo' }
  for (const s of [service('reviewer'), service('merge-queue'), service('updates'), service('doctor')]) await switchWrite(api, host, s)?.(false)
  assert.deepEqual(calls, [
    'review room_1 false',
    'merge-paused room_1 true',
    'service-edit updates {"disabled":true}',
    'service-edit doctor {"disabled":true}',
  ])
  assert.equal(switchWrite(api, host, service('library')), null)
})
