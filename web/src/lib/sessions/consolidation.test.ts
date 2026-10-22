import test from 'node:test'
import assert from 'node:assert/strict'
import { createConsolidationView, pruneSelection, toggleSelection, selectionKey, selectableSession, type ConsolidationApi, type ConsolidationContext, type ConsolidationValue } from './consolidation.ts'
import type { ConsolidationState } from '../api/session.ts'
import { need } from '../../../test/need.ts'
const sessions = [{ id: 'a', name: 'alpha', profile: 'old', model: 'old-model' }, { id: 'b', name: 'beta', profile: 'other' }]
const saved = (phase = 'waiting', extra: Partial<ConsolidationState> = {}): ConsolidationState => ({ id: 'op', roomId: 'room', target: 'a', sessions: ['a', 'b'], profile: 'old', model: 'old-model', phase, requested: '', deadline: '', sources: [], ...extra })
function deferred<T = never>() { let resolve: (value: T) => void = () => {}, reject: (error: Error) => void = () => {}; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
function setup(overrides: Partial<ConsolidationApi> = {}, context: Partial<ConsolidationContext> = {}) {
  let value: ConsolidationValue | undefined
  const calls: unknown[][] = []
  const api: ConsolidationApi = {
    profiles: async () => [{ name: 'new', harness: 'h', model: 'new-model' }],
    sessionConsolidations: async () => [],
    consolidateSessions: async (...args) => { calls.push(args); return saved('waiting', { target: args[1].target, profile: args[1].profile ?? 'old' }) },
    resumeSessionConsolidation: async (...args) => { calls.push(args); return saved('starting') },
    cancelSessionConsolidation: async (...args) => { calls.push(args); return saved('cancelled') },
    ...overrides,
  }
  const controller = createConsolidationView(api, { roomId: 'room', sessions, ...context }, (next) => { value = next })
  return { controller, calls, api, get value() { return need(value) } }
}

test('selection uses stable IDs, excludes owners/closed sessions, and prunes hierarchy changes without reordering', () => {
  const selected = ['b', 'a', 'b', 'closed', 'missing']
  assert.deepEqual(pruneSelection(selected, [...sessions].reverse()), ['b', 'a'])
  assert.deepEqual(selected, ['b', 'a', 'b', 'closed', 'missing'])
  assert.equal(selectableSession({ id: 'owner', owner: true }), false)
  assert.equal(selectableSession({ id: 'closed', closed: true }), false)
  assert.equal(selectableSession({ id: 'root', role: 'root' }), false)
  assert.deepEqual(toggleSelection(['a'], need(sessions[1]), true), ['a', 'b'])
  assert.deepEqual(toggleSelection(['a', 'b'], need(sessions[0]), false), ['b'])
  assert.deepEqual(toggleSelection(['a'], { id: 'closed', closed: true }, true), ['a'])
  const max = Array.from({ length: 16 }, (_, i) => String(i))
  assert.equal(toggleSelection(max, { id: 'extra' }, true).length, 17)
  assert.equal(selectionKey('room', ['a', 'b']), selectionKey('room', ['b', 'a']))
  assert.notEqual(selectionKey('room', ['a']), selectionKey('other', ['a']))
})
test('choosing a target or model never starts work; the explicit action defaults to the kept model', async () => {
  const c = setup(); await c.controller.load()
  c.controller.chooseProfile('new'); c.controller.chooseTarget('b')
  assert.equal(c.calls.length, 0); assert.equal(c.value.profile, '')
  await c.controller.start()
  assert.deepEqual(c.calls[0], ['room', { sessions: ['a', 'b'], target: 'b' }])
})
test('an optional configured model is sent only by the explicit start', async () => {
  const c = setup(); await c.controller.load(); c.controller.chooseProfile('new'); await c.controller.start()
  assert.deepEqual(need(c.calls[0])[1], { sessions: ['a', 'b'], target: 'a', profile: 'new' })
})
test('double submission creates one operation and a failed retry keeps its operation and model', async () => {
  const response = deferred<ConsolidationState>()
  const c = setup({ consolidateSessions: (...args) => { c.calls.push(args); return response.promise } })
  await c.controller.load(); c.controller.chooseProfile('new')
  const pending = c.controller.start(); await c.controller.start(); assert.equal(c.calls.length, 1)
  response.resolve(saved('failed', { profile: 'new', model: 'new-model' })); await pending
  c.controller.chooseProfile('old'); c.controller.chooseTarget('b'); await c.controller.resume()
  assert.deepEqual(c.calls[1], ['room', 'op']); assert.equal(c.value.target, 'a')
})
test('reopen discovers and resumes the same operation, even when selection order differs', async () => {
  const c = setup({ sessionConsolidations: async () => [saved()] }, { sessions: [...sessions].reverse() })
  await c.controller.load()
  assert.deepEqual(c.calls, [['room', 'op']]); assert.equal(need(c.value.operation).phase, 'starting')
  assert.equal(c.value.target, 'a')
})
test('closing during startup prevents all late updates and does not cancel durable work', async () => {
  const response = deferred<ConsolidationState>(); let lists = 0, cancels = 0
  const c = setup({ sessionConsolidations: async () => { lists++; return [] }, consolidateSessions: () => response.promise, cancelSessionConsolidation: async () => { cancels++; return saved('cancelled') } })
  await c.controller.load(); const pending = c.controller.start(); const before = c.value
  c.controller.destroy(); response.reject(new Error('worker launch delayed')); await pending
  assert.equal(c.value, before); assert.equal(lists, 1); assert.equal(cancels, 0)
})
test('a late discovery response cannot update a replacement room or selection view', async () => {
  const response = deferred<ConsolidationState[]>()
  const old = setup({ sessionConsolidations: () => response.promise }); const pending = old.controller.load()
  old.controller.destroy()
  const next = setup({}, { roomId: 'other', sessions: [{ id: 'x' }, { id: 'y' }] }); await next.controller.load()
  response.resolve([saved()]); await pending
  assert.equal(old.value.operation, null); assert.equal(next.value.operation, null); assert.equal(next.value.target, 'x')
})
test('a stale progress poll cannot overwrite cancellation, and stale errors stay silent', async () => {
  const response = deferred<ConsolidationState[]>(); let lists = 0
  const c = setup({ sessionConsolidations: () => ++lists === 1 ? Promise.resolve([saved('failed')]) : response.promise }, { operationId: 'op' })
  await c.controller.load(); const pending = c.controller.refresh(); await c.controller.cancel()
  response.reject(new Error('old connection')); await pending
  assert.equal(need(c.value.operation).phase, 'cancelled'); assert.equal(c.value.error, '')
})
test('polls reject different-room or different-operation responses without replacing state', async () => {
  let list = [saved('failed')]
  const c = setup({ sessionConsolidations: async () => list }, { operationId: 'op' }); await c.controller.load()
  list = [saved('completed', { roomId: 'other' })]; await c.controller.refresh()
  assert.equal(need(c.value.operation).phase, 'failed'); assert.match(c.value.error, /Couldn’t find/)
  list = [saved('completed', { id: 'different' })]; await c.controller.refresh()
  assert.equal(need(c.value.operation).id, 'op')
})
test('launch errors rediscover stored work and offer retry without another consolidation', async () => {
  let lists = 0
  const c = setup({ sessionConsolidations: async () => ++lists === 1 ? [] : [saved('failed')], consolidateSessions: async () => { throw new Error('launch failed') } })
  await c.controller.load(); await c.controller.start()
  assert.equal(need(c.value.operation).id, 'op'); assert.match(c.value.error, /launch failed/)
  await c.controller.resume(); assert.deepEqual(c.calls, [['room', 'op']])
})
test('missing saved operations and lookup errors cannot silently create new work', async () => {
  const c = setup({}, { operationId: 'missing' }); await c.controller.load(); await c.controller.start()
  assert.equal(c.calls.length, 0); assert.match(c.value.error, /unavailable/)
  const failed = setup({ sessionConsolidations: async () => { throw new Error('offline') } }); await failed.controller.load(); await failed.controller.start()
  assert.equal(failed.calls.length, 0); assert.match(failed.value.error, /saved consolidations/)
})

test('interrupted cancellation resumes the saved cleanup without a new model choice', async () => {
  const c = setup({ sessionConsolidations: async () => [saved('cancelled', { cancelPending: true })], resumeSessionConsolidation: async (...args) => { c.calls.push(args); return saved('cancelled') } }, { operationId: 'op' })
  await c.controller.load()
  assert.deepEqual(c.calls, [['room', 'op']]); assert.equal(need(c.value.operation).phase, 'cancelled')
})
