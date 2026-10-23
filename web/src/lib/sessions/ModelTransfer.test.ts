import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import type { TransferState } from '../api/session.ts'
import { call, runFunctions } from '../../../test/svelte-source.ts'
import { need } from '../../../test/need.ts'
import { isRecord } from '../api/guards.ts'

// Exercise the component's actual async handlers with deferred network calls.
const source = readFileSync(new URL('./ModelTransfer.svelte', import.meta.url), 'utf8')
function deferred<T = never>() {
  let resolve: (value: T) => void = () => {}, reject: (error: Error) => void = () => {}
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
// The component's state as the handlers read and write it.
interface Context {
  transfer: Partial<TransferState> | null
  api: Record<string, (...args: unknown[]) => unknown>
  loaded: boolean
  busy: boolean
  error: string
  roomId: string
  member: { id: string; profile?: string; model?: string; state?: string }
  transferRevision: number
  pollSerial: number
  [name: string]: unknown
}
function component(api: Context['api'], transfer: Context['transfer'] = { id: 'old', phase: 'waiting', profile: 'next', model: 'm' }): Context {
  const context: Context = { transfer, api, loaded: true, busy: false, error: '', roomId: 'room', member: { id: 'session' }, transferRevision: 0, pollSerial: 0 }
  Object.defineProperty(context, 'active', { get() { return ['waiting', 'stopping', 'starting'].includes(context.transfer?.phase ?? '') } })
  vm.createContext(context)
  runFunctions(source, ['isCurrent', 'unavailable', 'sessionId', 'refresh', 'start', 'resume', 'cancel'], context)
  return context
}

test('an old failed poll cannot overwrite a retry or stop progress polling', async () => {
  const oldPoll = deferred<Partial<TransferState>>()
  const c = component({ sessionTransfer: () => oldPoll.promise, transferSession: async () => ({ id: 'new', phase: 'waiting' }) })
  const pending = call(c, 'refresh')
  c.transfer = { id: 'old', phase: 'failed' }
  await call(c, 'start', { name: 'next', model: 'm' })
  oldPoll.resolve({ id: 'old', phase: 'failed' })
  await pending
  assert.equal(c.transfer?.id, 'new')
  assert.equal(c.active, true)
})

test('out-of-order polls keep the newest progress response', async () => {
  const first = deferred<Partial<TransferState>>(), second = deferred<Partial<TransferState>>()
  let calls = 0
  const c = component({ sessionTransfer: () => (++calls === 1 ? first.promise : second.promise) })
  const p1 = call(c, 'refresh'), p2 = call(c, 'refresh')
  second.resolve({ id: 'old', phase: 'starting' })
  await p2
  first.resolve({ id: 'old', phase: 'waiting' })
  await p1
  assert.equal(c.transfer?.phase, 'starting')
})

test('a stale poll error cannot overwrite successful cancellation', async () => {
  const oldPoll = deferred<Partial<TransferState>>()
  const c = component({ sessionTransfer: () => oldPoll.promise, cancelSessionTransfer: async () => ({ id: 'old', phase: 'cancelled' }) })
  const pending = call(c, 'refresh')
  await call(c, 'cancel')
  oldPoll.reject(new Error('stale network error'))
  await pending
  assert.equal(c.transfer?.phase, 'cancelled')
  assert.equal(c.error, '')
})

test('resuming a saved coordinator invalidates in-flight status responses', async () => {
  const oldPoll = deferred<Partial<TransferState>>()
  const c = component({ sessionTransfer: () => oldPoll.promise, transferSession: async () => ({ id: 'old', phase: 'waiting' }) })
  const pending = call(c, 'refresh')
  await call(c, 'resume')
  oldPoll.resolve({ id: 'old', phase: 'failed' })
  await pending
  assert.equal(c.transfer?.phase, 'waiting')
  assert.equal(c.active, true)
})


test('one model selection starts one transfer with the fixed 30 second wait', async () => {
  const response = deferred<Partial<TransferState>>()
  const calls: unknown[][] = []
  const c = component({ transferSession: (...args) => { calls.push(args); return response.promise } }, null)
  const choice = { name: 'next', model: 'm' }
  const pending = call(c, 'start', choice)
  await call(c, 'start', choice)
  assert.equal(calls.length, 1)
  assert.deepEqual(JSON.parse(JSON.stringify(calls[0])), ['room', 'session', { profile: 'next', waitSeconds: 30, retry: false }])
  response.resolve({ id: 'new', phase: 'waiting', profile: 'next' })
  await pending
  await call(c, 'start', choice)
  assert.equal(calls.length, 1)
})

test('a failed replacement offers another model or a retry in the same picker', async () => {
  const calls: unknown[][] = []
  const c = component({ transferSession: async (...args) => { calls.push(args); return { id: 'retry', phase: 'waiting', profile: 'other' } } }, { phase: 'failed', profile: 'next' })
  await call(c, 'start', { name: 'other' })
  const options = need(calls[0])[2]
  assert.ok(isRecord(options))
  assert.equal(options.retry, true)
  assert.equal(options.profile, 'other')
  assert.equal(c.transfer?.id, 'retry')
})

test('a saved worker error resumes the same attempt through its model choice', async () => {
  const calls: unknown[][] = []
  const c = component({ transferSession: async (...args) => { calls.push(args); return { id: 'saved', phase: 'waiting', profile: 'next' } } }, { id: 'saved', phase: 'waiting', profile: 'next', error: 'worker unavailable' })
  c.member = { id: 'session', profile: 'next', model: 'm', state: 'idle' }
  await call(c, 'start', { name: 'other' })
  assert.equal(calls.length, 0)
  await call(c, 'start', { name: 'next' })
  assert.equal(calls.length, 1)
  const options = need(calls[0])[2]
  assert.ok(isRecord(options))
  assert.equal(options.retry, undefined)
  assert.equal(c.transfer?.id, 'saved')
})

test('unmount invalidates a pending mutation and prevents a later refresh', async () => {
  const response = deferred<Partial<TransferState>>()
  let polls = 0
  const c = component({ transferSession: () => response.promise, sessionTransfer: () => { polls++; return null } }, null)
  const pending = call(c, 'start', { name: 'next' })
  c.transferRevision++
  response.reject(new Error('late error'))
  await pending
  assert.equal(polls, 0)
  assert.equal(c.error, '')
  assert.equal(c.transfer, null)
})

test('a completed choice stays disabled even when the old member snapshot failed', async () => {
  let calls = 0
  const c = component({ transferSession: () => { calls++ } }, { phase: 'completed', profile: 'next', model: 'm' })
  c.member.state = 'error'
  await call(c, 'start', { name: 'next', model: 'm' })
  assert.equal(calls, 0)
})

test('a successful poll clears a temporary progress error', async () => {
  let calls = 0
  const c = component({ sessionTransfer: async () => { if (++calls === 1) throw new Error('offline'); return { phase: 'starting' } } })
  await call(c, 'refresh')
  assert.match(c.error, /Couldn’t check/)
  await call(c, 'refresh')
  assert.equal(c.error, '')
  assert.equal(c.transfer?.phase, 'starting')
})
