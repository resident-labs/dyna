import assert from 'node:assert/strict'
import test from 'node:test'
import { groupRows, healthyLine, hookRows, logKey, readLog, serviceRows, settleReads } from './servicesCard.ts'
import type { Hook, HookRun, HookSummary } from '../api/settings.ts'
import type { ServiceView } from '../api/service.ts'

const hook = (over: Partial<Hook>): Hook => ({ name: 'maintenance', event: 'schedule', command: '', cwd: '', timeout: '5m', onFail: 'alert', scope: 'global', addedBy: 'dyna', addedAt: '', ...over })
const run = (result: string, id = 'r1'): HookRun => ({ id, repo: '', name: 'maintenance', command: '', result, logPath: '', startedAt: '2026-10-26T10:00:00Z' })
const summary = (h: Partial<Hook>, last?: HookRun): HookSummary => ({ hook: hook(h), lastRun: last })
const service = (over: Partial<ServiceView>): ServiceView => ({ role: 'librarian', name: 'Librarian', page: '', profile: 'sol', pinned: '', source: '', reason: '', waiting: false, scope: 'global', sessions: 0, profiles: [], ...over })

test('a failed hook run is failing, a pass is healthy, and its log is that run', () => {
  const [bad, ok] = hookRows([summary({ name: 'a' }, run('error', 'x')), summary({ name: 'b' }, run('pass'))])
  assert.deepEqual([bad?.tone, bad?.state, bad?.log], ['bad', 'Failed', { kind: 'hook', hook: 'a', runId: 'x' }])
  assert.deepEqual([ok?.tone, ok?.state], ['ok', 'Passed'])
})

test('a hook that is off or never ran is quiet, and the doctor trigger is not listed', () => {
  const rows = hookRows([summary({ name: 'a', disabled: true }, run('error')), summary({ name: 'b' }), summary({ name: 'doctor', builtin: 'doctor' }, run('error'))])
  assert.deepEqual(rows.map((r) => [r.id, r.tone, r.state]), [['hook:a', 'idle', 'Off'], ['hook:b', 'idle', 'Never run']])
})

test('a service with no usable profile fails in any room, and its log opens that room', () => {
  const rows = serviceRows([
    { room: 'one', services: [service({})] },
    { room: 'two', services: [service({ profile: '', reason: 'no enabled librarian profile is available' })] },
    { room: 'three', services: [service({})] },
  ])
  assert.equal(rows.length, 1)
  assert.deepEqual([rows[0]?.tone, rows[0]?.state, rows[0]?.log], ['bad', 'no enabled librarian profile is available', { kind: 'room', room: 'two' }])
})

test('failing rows come first and the healthy rows fold into one line', () => {
  const rows = [...hookRows([summary({ name: 'ok' }, run('pass')), summary({ name: 'bad' }, run('fail'))]), ...serviceRows([{ room: 'one', services: [service({})] }])]
  const { failing, healthy } = groupRows(rows)
  assert.deepEqual(failing.map((r) => r.id), ['hook:bad'])
  assert.equal(healthy.length, 2)
  assert.equal(healthyLine(8, 0), '8 running normally')
  assert.equal(healthyLine(2, 1), '2 others running normally')
})

test('a failed global run opens its log, and a failed read shows the reason', async () => {
  const [bad] = hookRows([summary({ name: 'maintenance' }, run('error', 'x'))])
  assert.equal(bad?.log?.kind, 'hook')
  if (bad?.log?.kind !== 'hook') return
  const seen: string[] = []
  const tail = await readLog(async (hook, id) => { seen.push(`${hook}/${id}`); return { tail: 'disk read failed', exitCode: 1 } }, bad.log)
  assert.deepEqual([tail, seen], ['disk read failed', ['maintenance/x']])
  assert.equal(await readLog(async () => { throw new Error('log is gone') }, bad.log), 'log is gone')
})

test('a newer run of the same hook has its own log key', () => {
  const [a] = hookRows([summary({ name: 'maintenance' }, run('error', 'old'))])
  const [b] = hookRows([summary({ name: 'maintenance' }, run('error', 'new'))])
  if (a?.log?.kind !== 'hook' || b?.log?.kind !== 'hook') return assert.fail('hook rows carry hook logs')
  assert.notEqual(logKey(a.log), logKey(b.log))
})

test('a failed room read keeps that room last good list, and a room never read shows as unreadable', () => {
  const bad = service({ profile: '', reason: 'no profile' })
  const first = settleReads({}, [{ room: 'one', services: [bad] }, { room: 'two', services: [service({})] }])
  const again = settleReads(first.kept, [{ room: 'one', services: null }, { room: 'two', services: [service({})] }])
  assert.deepEqual(serviceRows(again.byRoom).map((r) => r.state), ['Could not refresh; showing the last read', 'no profile'])
  const fresh = settleReads({}, [{ room: 'one', services: null }, { room: 'two', services: [service({})] }])
  assert.deepEqual(serviceRows(fresh.byRoom).map((r) => [r.id, r.tone, r.state]), [['service:unreadable:one', 'bad', 'Could not be read'], ['service:librarian', 'ok', 'sol']])
})

test('a healthy read followed by a failed refresh keeps the rows and warns', () => {
  const first = settleReads({}, [{ room: 'one', services: [service({})] }])
  const again = settleReads(first.kept, [{ room: 'one', services: null }])
  assert.deepEqual(serviceRows(again.byRoom).map((r) => [r.id, r.tone]), [['service:stale:one', 'bad'], ['service:librarian', 'ok']])
  const recovered = settleReads(again.kept, [{ room: 'one', services: [service({})] }])
  assert.deepEqual(serviceRows(recovered.byRoom).map((r) => r.id), ['service:librarian'])
})
