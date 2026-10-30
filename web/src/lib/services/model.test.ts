import assert from 'node:assert/strict'
import test from 'node:test'
import { factsOf, pillOf, sections } from './model.ts'
import type { RegistryService } from '../api/service.ts'

const service = (s: Partial<RegistryService> & Pick<RegistryService, 'id' | 'group'>): RegistryService =>
  ({ label: s.id, description: '', state: 'on', detail: '', count: 0, profile: '', session: '', hook: '', settingsKeys: [], ...s })

test('services group by their registry group in registry order, and not-set-up ones go last', () => {
  const list = [
    service({ id: 'library', group: 'agents' }),
    service({ id: 'lane-sync', group: 'pull-requests', state: 'not-set-up' }),
    service({ id: 'reviewer', group: 'pull-requests' }),
    service({ id: 'doctor', group: 'this-computer' }),
    service({ id: 'voice', group: 'agents' }),
  ]
  assert.deepEqual(sections(list).map((s) => [s.id, s.services.map((x) => x.id)]), [
    ['agents', ['library', 'voice']],
    ['pull-requests', ['reviewer']],
    ['this-computer', ['doctor']],
    ['not-set-up', ['lane-sync']],
  ])
})

test('only a failed service gets the coral pill; quiet and working states stay muted text and "On" never shows', () => {
  const now = Date.parse('2026-10-30T03:00:00')
  const failed = service({ id: 'updates', group: 'this-computer', state: 'error', detail: 'last-run-failed', switch: { on: true } })
  assert.deepEqual(pillOf(failed), { label: 'Last run failed', tone: 'danger' })
  const healthy = service({ id: 'doctor', group: 'this-computer', switch: { on: true }, nextRun: '2026-10-30T03:02:00' })
  assert.equal(pillOf(healthy), null)
  assert.deepEqual(factsOf(healthy, now, true), ['Next 03:02'])
  const library = service({ id: 'library', group: 'agents', state: 'idle', profile: 'glm-flash', switch: { on: true } })
  assert.equal(pillOf(library), null)
  assert.deepEqual(factsOf(library, now, true), ['Idle', 'glm-flash'])
  // A switch the app cannot write is words, not a switch that never moves.
  assert.deepEqual(factsOf(library, now, false), ['On for this repository', 'Idle', 'glm-flash'])
  const working = service({ id: 'reviewer', group: 'pull-requests', state: 'running', detail: 'working', count: 1, switch: { on: true } })
  assert.equal(pillOf(working), null)
  assert.deepEqual(factsOf(working, now, true), ['Working · 1 waiting'])
})
