import assert from 'node:assert/strict'
import test from 'node:test'
import { groupRows, healthyLine, hookRows, logKey, readLog, serviceRows } from './servicesCard.ts'
import type { Hook, HookRun, HookSummary } from '../api/settings.ts'
import type { RegistryService } from '../api/service.ts'

const hook = (over: Partial<Hook>): Hook => ({ name: 'maintenance', event: 'schedule', command: '', cwd: '', timeout: '5m', onFail: 'alert', scope: 'global', addedBy: 'dyna', addedAt: '', ...over })
const run = (result: string, id = 'r1'): HookRun => ({ id, repo: '', name: 'maintenance', command: '', result, logPath: '', startedAt: '2026-10-26T10:00:00Z' })
const summary = (h: Partial<Hook>, last?: HookRun): HookSummary => ({ hook: hook(h), lastRun: last })
const service = (over: Partial<RegistryService>): RegistryService => ({ id: 'doctor', group: 'this-computer', scope: 'global', label: 'Load doctor', description: '', state: 'on', detail: '', count: 0, profile: '', session: '', settingsKeys: [], ...over })

test('a failed hook run is failing, a pass is healthy, and its log is that run', () => {
  const [bad, ok] = hookRows([summary({ name: 'a' }, run('error', 'x')), summary({ name: 'b' }, run('pass'))])
  assert.deepEqual([bad?.tone, bad?.state, bad?.log], ['bad', 'Failed', { kind: 'hook', hook: 'a', runId: 'x' }])
  assert.deepEqual([ok?.tone, ok?.state], ['ok', 'Passed'])
})

test('a hook that is off or never ran is quiet, and the doctor trigger is not listed', () => {
  const rows = hookRows([summary({ name: 'a', disabled: true }, run('error')), summary({ name: 'b' }), summary({ name: 'doctor', builtin: 'doctor' }, run('error'))])
  assert.deepEqual(rows.map((r) => [r.id, r.tone, r.state]), [['hook:a', 'idle', 'Off'], ['hook:b', 'idle', 'Never run']])
})

test('Home lists only global registry services', () => {
  const rows = serviceRows([service({}), service({ id: 'reviewer', scope: 'room', label: 'Reviews' }), service({ id: 'library', scope: 'room', label: 'Library', state: 'error', detail: 'last-run-failed' })])
  assert.deepEqual(rows.map((r) => [r.name, r.log]), [['Load doctor', { kind: 'settings' }]])
})

test('failing rows come first and the healthy rows fold into one line', () => {
  const rows = [...hookRows([summary({ name: 'ok' }, run('pass')), summary({ name: 'bad' }, run('fail'))]), ...serviceRows([service({})])]
  const { failing, healthy } = groupRows(rows)
  assert.deepEqual(failing.map((r) => r.id), ['hook:bad'])
  assert.equal(healthy.length, 2)
  assert.equal(healthyLine(8, 0), '8 services')
  assert.equal(healthyLine(2, 1), '2 other services')
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
