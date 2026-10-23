import test from 'node:test'
import assert from 'node:assert/strict'
import { createRoomSessionControls, roomControlResult, roomControlSummary, sessionName } from './roomSessionControls.ts'
import type { RoomControlValue, RoomControlsApi } from './roomSessionControls.ts'
import { need } from '../../../test/need.ts'

const state = (paused = false) => ({ roomId: 'a', paused, running: 1, held: 2, dormant: 3 })
// The calls a test does not name still answer, so the controller sees a whole host.
const api = (calls: Partial<RoomControlsApi>): RoomControlsApi => ({
  roomSessionControls: async () => state(),
  pauseRoomSessions: async () => ({ state: state(true), results: [] }),
  resumeRoomSessions: async () => ({ state: state(), results: [] }),
  clearDormantSessions: async () => ({ state: state(), results: [] }),
  ...calls,
})
function deferred<T = unknown>() { let resolve: (value: T) => void = () => {}; let reject: (reason?: unknown) => void = () => {}; const promise = new Promise<T>((a, b) => { resolve = a; reject = b }); return { promise, resolve, reject } }
function view(calls: Partial<RoomControlsApi>) {
  let value: RoomControlValue | undefined
  const control = createRoomSessionControls(api(calls), 'a', (v) => { value = v })
  return { control, get value(): RoomControlValue { return need(value) } }
}

test('pause keeps ordered refusals and invalidates an older unpaused status read', async () => {
  const old = deferred(), pause = deferred(); let calls = 0
  const v = view({ roomSessionControls: () => { calls++; return old.promise }, pauseRoomSessions: () => pause.promise })
  const reading = v.control.load(), trailing = v.control.load(); assert.equal(calls, 1)
  const running = v.control.run('pause'); await v.control.run('resume')
  pause.resolve({ state: state(true), results: [{ session: 'z-id', ok: false, status: 'refused', error: 'Reserved' }, { session: 'a-id', ok: true, status: 'paused' }] })
  await running; old.resolve(state(false)); await Promise.all([reading, trailing])
  assert.equal(v.value.state?.paused, true); assert.equal(v.value.busy, false)
  assert.deepEqual(v.value.results.map((r) => r.session), ['z-id', 'a-id'])
})
test('resume can recover held work even when room status already says unpaused', async () => {
  let calls = 0
  const v = view({ roomSessionControls: async () => state(false), resumeRoomSessions: async (id) => { assert.equal(id, 'a'); calls++; return { state: state(false), results: [{ session: 'held', ok: true, status: 'resumed' }] } } })
  await v.control.load(); await v.control.run('resume'); await v.control.run('resume')
  assert.equal(calls, 2); assert.equal(v.value.results[0]?.session, 'held')
})
test('clearDormant delegates to clearDormantSessions and reports closed results', async () => {
  const results = [{ session: 'done-a', ok: true, status: 'closed' }, { session: 'done-b', ok: false, status: 'kept', error: 'responsibilities: cannot close a dormant session' }]
  const v = view({ clearDormantSessions: async (id) => { assert.equal(id, 'a'); return { state: { ...state(), dormant: 1 }, results } } })
  await v.control.run('clearDormant'); assert.deepEqual(v.value.results, results)
  assert.equal(v.value.action, 'clearDormant')
  assert.equal(roomControlResult(need(results[0]), 'clearDormant'), 'Closed')
  assert.equal(roomControlResult(need(results[1]), 'clearDormant'), 'responsibilities: cannot close a dormant session')
})
test('teardown suppresses late mutation and status callbacks without a cancellation call', async () => {
  for (const method of ['load', 'run']) {
    const response = deferred(); let value: RoomControlValue | undefined, changes = 0
    const v = createRoomSessionControls(api({ roomSessionControls: () => response.promise, pauseRoomSessions: () => response.promise }), 'a', (v) => { value = v; changes++ })
    const pending = method === 'load' ? v.load() : v.run('pause'); v.destroy(); const before = changes
    response.resolve(method === 'load' ? state() : { state: state(true), results: [] }); await pending
    assert.equal(changes, before); assert.equal(need(value).state, null)
  }
})
test('wrong-room status/results are rejected, and refresh does not hide mutation uncertainty', async () => {
  const v = view({ roomSessionControls: async () => state(), pauseRoomSessions: async () => ({ state: { ...state(true), roomId: 'b' }, results: [] }) })
  await v.control.run('pause'); assert.match(v.value.error, /incomplete result/)
  await v.control.load(); assert.equal(v.value.state?.paused, false); assert.match(v.value.error, /incomplete result/)
  const wrong = view({ roomSessionControls: async () => ({ ...state(), roomId: 'b' }) }); await wrong.control.load(); assert.equal(wrong.value.state, null); assert.match(wrong.value.statusError, /valid session controls/)
})

test('finite backend statuses use readable labels and keep explicit refusal reasons', () => {
  assert.equal(roomControlResult({ ok: true, status: 'running' }, 'pause'), 'Running turn continues')
  assert.equal(roomControlResult({ ok: true, status: 'closed' }, 'clearDormant'), 'Closed')
  assert.equal(roomControlResult({ ok: false, status: 'kept', error: 'Open children' }, 'clearDormant'), 'Open children')
  assert.equal(roomControlResult({ ok: false, status: 'kept' }, 'clearDormant'), 'Kept open')
})

test('clear dormant reports one short line, and a kept session is not an error', () => {
  const results = [
    { session: 's1', name: 'dyna:fix-flaky-tests', ok: true, status: 'closed' },
    { session: 's2', name: 'dyna:ts-guard', ok: true, status: 'kept', reason: 'started-work' as const },
    { session: 's3', name: 'dyna:ts-leftovers', ok: true, status: 'kept', reason: 'started-work' as const },
    { session: 's4', name: 'dyna:member-status', ok: false, status: 'kept', reason: 'failed' as const, error: 'the store lock is gone' },
  ]
  const value: RoomControlValue = { state: null, loading: false, busy: false, action: 'clearDormant', results, error: '', statusError: '' }
  const summary = roomControlSummary(value)
  assert.equal(summary.text, 'Closed 1 dormant session. 2 kept because they had new work. 1 session could not be closed.')
  assert.deepEqual(summary.details.filter((detail) => detail.error).map((detail) => detail.name), ['dyna:member-status'])
  assert.equal(summary.details[1]?.text, 'Kept open · it gained new work')
})

test('a result shows a session by name and never shows a raw session ID', () => {
  assert.equal(sessionName({ session: 'ses_20261023-105413-cc5dd68c', ok: false, status: 'kept' }), 'session cc5dd68c')
  assert.equal(sessionName({ session: 'ses_1', ok: true, status: 'closed' }, [{ id: 'ses_1', address: 'dyna:lead' }]), 'dyna:lead')
  assert.equal(sessionName({ session: 'ses_2', name: 'dyna:root', ok: true, status: 'closed' }), 'dyna:root')
})

test('missing or invalid status counts cannot enable a destructive action', async () => {
  for (const dormant of [undefined, -1, 1.5, Infinity]) {
    const v = view({ roomSessionControls: async () => ({ ...state(), dormant }) })
    await v.control.load(); assert.equal(v.value.state, null); assert.ok(v.value.statusError)
  }
})
