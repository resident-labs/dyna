import test from 'node:test'
import assert from 'node:assert/strict'
import type { RunDetail, RunResultView } from '../api/workflow.ts'
import { createResultLoader, createWorkerResults, type LoadedResult, type WorkerRow, type WorkerRows } from './runResult.ts'

const tick = async () => { await Promise.resolve(); await Promise.resolve() }
const result = (extra: Partial<RunResultView> = {}): RunResultView => ({ result: '', bytes: 0, ...extra })
const detailOf = (id: string, resultBytes?: number): Pick<RunDetail, 'run' | 'resultBytes'> => ({
  run: { id, name: id, status: 'ok', startedAt: '', roomId: 'r', agents: 0, done: 0, failed: 0, working: 0 },
  resultBytes,
})
// rowOf is the open row for one agent, or a failed assertion.
function rowOf(state: WorkerRows, agent: number): WorkerRow {
  const row = state[agent]
  assert.ok(row, `row ${agent} exists`)
  return row
}

test('the run result loads once per run and size, and a stale answer is dropped', async () => {
  const calls: [string, number][] = []
  const answers: Record<string, (v: RunResultView) => void> = {}
  const api = { runResult: (id: string, agent: number) => { calls.push([id, agent]); return new Promise<RunResultView>((resolve) => (answers[id] = resolve)) } }
  const seen: LoadedResult[] = []
  const load = createResultLoader(api, (v) => seen.push(v))
  load(detailOf('wf_a', 10))
  load(detailOf('wf_a', 10))
  assert.deepEqual(calls, [['wf_a', 0]])
  load(detailOf('wf_b', 5))
  answers.wf_a?.(result({ result: '{"a":1}', bytes: 10 }))
  answers.wf_b?.(result({ result: '{"b":2}', bytes: 5 }))
  await tick()
  assert.deepEqual(seen.map((v) => (v === null || 'error' in v ? null : v.result)), [null, '{"b":2}'])
  load(detailOf('wf_c'))
  assert.equal(seen.at(-1), null, 'a run without a result clears it')
})

test('a worker result loads once when its row opens, and a reopened row reuses it', async () => {
  const calls: [string, number][] = []
  const api = { runResult: async (id: string, agent: number) => { calls.push([id, agent]); return result({ result: `{"n":${agent}}` }) } }
  let state: WorkerRows = {}
  const workers = createWorkerResults(api, (s) => (state = s))
  workers.toggle('wf_a', 2)
  assert.equal(rowOf(state, 2).open, true)
  assert.equal(rowOf(state, 2).loading, true)
  await tick()
  assert.equal(rowOf(state, 2).value?.result, '{"n":2}')
  workers.toggle('wf_a', 2)
  assert.equal(rowOf(state, 2).open, false)
  workers.toggle('wf_a', 2)
  assert.deepEqual(calls, [['wf_a', 2]], 'reopening does not load again')
  workers.reset()
  assert.deepEqual(state, {})
})

test('a detail refresh for the same run keeps open rows and their results', async () => {
  let state: WorkerRows = {}
  const workers = createWorkerResults({ runResult: async () => result({ result: '{"a":1}' }) }, (s) => (state = s))
  workers.sync('wf_a')
  workers.toggle('wf_a', 1)
  await tick()
  workers.sync('wf_a')
  assert.equal(rowOf(state, 1).open, true)
  assert.equal(rowOf(state, 1).value?.result, '{"a":1}')
  workers.sync('wf_b')
  assert.deepEqual(state, {}, 'another run starts with no open rows')
})

test('an open running worker refreshes with the run, fetches its final result once, and a failed load retries', async () => {
  let n = 0
  let fail = false
  const api = { runResult: async () => { n++; if (fail) throw new Error('down'); return result({ journal: Array.from({ length: n }, (_, i) => ({ ts: i, kind: 'progress', message: '' })) }) } }
  let state: WorkerRows = {}
  const w = createWorkerResults(api, (s) => (state = s))
  w.sync('wf_a')
  w.toggle('wf_a', 1, 'running')
  await tick()
  assert.equal(rowOf(state, 1).value?.journal?.length, 1)
  w.refresh('wf_a', [{ id: 1, status: 'running' }])
  await tick()
  assert.equal(rowOf(state, 1).value?.journal?.length, 2, 'the journal grows while the row is open')
  w.refresh('wf_a', [{ id: 1, status: 'ok' }])
  await tick()
  assert.equal(n, 3, 'the finish fetches the final result')
  w.refresh('wf_a', [{ id: 1, status: 'ok' }])
  w.toggle('wf_a', 1, 'ok'); w.toggle('wf_a', 1, 'ok')
  await tick()
  assert.equal(n, 3, 'a finished worker stays cached')
  fail = true
  w.toggle('wf_a', 2, 'ok')
  await tick()
  assert.match(rowOf(state, 2).error ?? '', /down/)
  fail = false
  w.toggle('wf_a', 2, 'ok'); w.toggle('wf_a', 2, 'ok')
  await tick()
  assert.equal(rowOf(state, 2).error, undefined)
  assert.ok(rowOf(state, 2).value, 'a failed load retries when the row reopens')
})

test('a finish that arrives during a load fetches the final result after it', async () => {
  const pending: ((v: RunResultView) => void)[] = []
  const api = { runResult: () => new Promise<RunResultView>((resolve) => pending.push(resolve)) }
  let state: WorkerRows = {}
  const w = createWorkerResults(api, (s) => (state = s))
  w.sync('wf_a')
  w.toggle('wf_a', 1, 'running')
  w.refresh('wf_a', [{ id: 1, status: 'ok' }])
  assert.equal(pending.length, 1, 'no second request while one is in flight')
  pending[0]?.(result())
  await tick()
  assert.equal(pending.length, 2, 'the queued finish fetches again')
  pending[1]?.(result({ result: '{"done":true}' }))
  await tick()
  assert.equal(rowOf(state, 1).final, true)
  assert.equal(rowOf(state, 1).value?.result, '{"done":true}')
})

test('explicit result retries recover failures and reject stale replies after returning to a run', async () => {
  const requests: { resolve: (value: RunResultView) => void; reject: (error: Error) => void }[] = []
  const api = { runResult: () => new Promise<RunResultView>((resolve, reject) => requests.push({ resolve, reject })) }
  let value: LoadedResult = null
  const load = createResultLoader(api, (next) => { value = next })
  load(detailOf('a', 10))
  requests[0]?.reject(new Error('offline'))
  await tick()
  assert.deepEqual(value, { error: 'offline' })
  load(detailOf('a', 10))
  assert.equal(requests.length, 1)
  load.retry(detailOf('a', 10))
  load(detailOf('b', 10))
  load(detailOf('a', 10))
  requests[1]?.resolve(result({ result: 'old retry' }))
  await tick()
  assert.equal(value, null)
  requests[3]?.resolve(result({ result: 'current' }))
  await tick()
  assert.deepEqual(value, result({ result: 'current' }))

  let rows: WorkerRows = {}
  const workers = createWorkerResults(api, (next) => { rows = next })
  workers.sync('a')
  workers.toggle('a', 1, 'running')
  requests[4]?.reject(new Error('offline'))
  await tick()
  workers.retry('a', 1, 'running')
  workers.retry('a', 1, 'running')
  assert.equal(requests.length, 6, 'retry does not overlap a pending read')
  requests[5]?.resolve(result({ result: 'saved' }))
  await tick()
  workers.refresh('a', [{ id: 1, status: 'running' }])
  assert.equal(rowOf(rows, 1).value?.result, 'saved')
  assert.equal(rowOf(rows, 1).loading, true)
  workers.sync('b')
  workers.sync('a')
  workers.toggle('a', 1, 'running')
  requests[6]?.resolve(result({ result: 'stale refresh' }))
  await tick()
  assert.equal(rowOf(rows, 1).value, undefined)
  requests[7]?.resolve(result({ result: 'new worker result' }))
  await tick()
  assert.equal(rowOf(rows, 1).value?.result, 'new worker result')
})
