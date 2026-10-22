import assert from 'node:assert/strict'
import test from 'node:test'
import { get } from 'svelte/store'
import { parseAppEvent } from '../api/guards.ts'
import * as attention from './attention.ts'
import { callLink, callTarget, createCallRequests, ringing, type CallRequest, type CallRequestsApi } from './callRequests.ts'

const req = (id: string, state: CallRequest['state'] = 'ringing', room = 'room_1', askedAt = '2026-10-20T10:00:00Z'): CallRequest => ({ id, room, reason: 'CI is red', state, askedAt })
const upsert = (item: CallRequest) => ({ kind: 'call-requests', roomId: item.room, change: { op: 'upsert', id: item.id, item } })

function fake(grouped: Record<string, CallRequest[]> = {}) {
  const calls: string[] = []
  const api: CallRequestsApi = {
    callRequests: async (room) => (calls.push(`list ${room}`), grouped),
    callRequestShown: async (room, id) => (calls.push(`shown ${room}/${id}`), {}),
    declineCallRequest: async (room, id) => (calls.push(`decline ${room}/${id}`), req(id, 'declined', room)),
  }
  return { calls, requests: createCallRequests(api) }
}

test('a post to @voice-call no longer rings', () => {
  assert.equal('ringFrom' in attention, false)
  assert.equal(attention.rootToVoice({ from: 'voice-test:root', to: ['voice-test:voice-call'] }), true, 'still root speaking to Voice in a call')
  const { requests } = fake()
  requests.apply({ kind: 'posts', roomId: 'room_1', posts: [{ id: 'm1', from: 'voice-test:root', to: ['voice-test:voice-call'], text: 'CI is failing.' }] })
  assert.equal(ringing(get(requests)), null)
})

test('a ringing request in a live event rings, and a closing event stops it', () => {
  const { requests } = fake()
  requests.apply(upsert(req('c7')))
  assert.equal(ringing(get(requests))?.id, 'c7')
  requests.apply(upsert(req('c7', 'joined')))
  assert.equal(ringing(get(requests)), null)
  assert.equal(get(requests).length, 1, 'the same request, updated')
})

test('a ringing request from the load rings, the oldest first', async () => {
  const { requests, calls } = fake({ room_1: [req('c2', 'ringing', 'room_1', '2026-10-20T10:05:00Z')], room_2: [req('c1', 'ringing', 'room_2')] })
  await requests.load()
  assert.deepEqual(calls, ['list '])
  assert.equal(ringing(get(requests))?.id, 'c1')
})

test('a load never reopens a request that an event closed', async () => {
  const { requests } = fake({ room_1: [req('c7')] })
  requests.apply(upsert(req('c7', 'declined')))
  await requests.load('room_1')
  assert.equal(get(requests)[0]?.state, 'declined')
})

test('events of other kinds and malformed requests are ignored', () => {
  const { requests } = fake()
  requests.apply({ kind: 'questions', roomId: 'room_1', change: { op: 'upsert', id: 'q1', item: req('c7') } })
  requests.apply(upsert({ ...req('c7'), id: 'x7' }))
  requests.apply(upsert({ ...req('c7'), state: 'busy' as CallRequest['state'] }))
  assert.deepEqual(get(requests), [])
})

test('the engine hears that a request was shown once', async () => {
  const { requests, calls } = fake()
  await requests.shown(req('c7'))
  await requests.shown(req('c7'))
  await requests.shown(req('c8'))
  assert.deepEqual(calls, ['shown room_1/c7', 'shown room_1/c8'])
})

test('Decline tells the engine and closes the request here', async () => {
  const { requests, calls } = fake()
  requests.apply(upsert(req('c7')))
  await requests.decline(req('c7'))
  assert.deepEqual(calls, ['decline room_1/c7'])
  assert.equal(ringing(get(requests)), null)
  assert.equal(get(requests)[0]?.state, 'declined')
})

test('a push link carries a valid call ID only', () => {
  assert.deepEqual(callTarget('#room=room_20261020-103000-0a1b2c3d&call=c7'), { room: 'room_20261020-103000-0a1b2c3d', call: 'c7' })
  assert.deepEqual(callTarget('#room=room_20261020-103000-0a1b2c3d&post=p1&call=c123456'), { room: 'room_20261020-103000-0a1b2c3d', call: 'c123456' })
  for (const bad of ['#room=room_20261020-103000-0a1b2c3d&call=7', '#room=room_20261020-103000-0a1b2c3d&call=c1234567', '#room=room_20261020-103000-0a1b2c3d&call=c7x', '#call=c7', '#room=room_20261020-103000-0a1b2c3d']) assert.equal(callTarget(bad), null, bad)
  assert.equal(callLink('room_20261020-103000-0a1b2c3d', null), null)
})

test('resync clears a request closed while the app was offline', async () => {
  const { requests } = fake()
  requests.apply(upsert(req('c7')))
  await requests.load()
  assert.equal(ringing(get(requests)), null)
})

test('the transport parser admits a call-request list event', () => {
  assert.deepEqual(parseAppEvent(upsert(req('c7'))), upsert(req('c7')))
})

test('Decline keeps the engine state when another device joined first', async () => {
  const requests = createCallRequests({
    callRequests: async () => ({}),
    callRequestShown: async () => ({}),
    declineCallRequest: async () => req('c7', 'joined'),
  })
  requests.apply(upsert(req('c7')))
  await requests.decline(req('c7'))
  assert.equal(get(requests)[0]?.state, 'joined')
})

test('a snapshot in flight cannot overwrite a newer ringing event', async () => {
  let reply: (value: unknown) => void = () => {}
  const requests = createCallRequests({
    callRequests: () => new Promise((resolve) => { reply = resolve }),
    callRequestShown: async () => ({}),
    declineCallRequest: async () => ({}),
  })
  const pending = requests.load()
  requests.apply(upsert({ ...req('c7'), reason: 'Updated reason' }))
  reply({ room_1: [req('c7')] })
  await pending
  assert.equal(get(requests)[0]?.reason, 'Updated reason')
})

test('push links reject non-string call IDs and invalid rooms', () => {
  assert.equal(callLink('room_20261020-103000-0a1b2c3d', ['c7']), null)
  assert.equal(callLink('not-a-room', 'c7'), null)
})

test('resync reads the closed state for a room row after a missed event', async () => {
  const calls: string[] = []
  const requests = createCallRequests({
    callRequests: async (room) => {
      calls.push(room)
      return room ? { room_1: [req('c7', 'withdrawn')] } : {}
    },
    callRequestShown: async () => ({}),
    declineCallRequest: async () => ({}),
  })
  requests.apply(upsert(req('c7')))
  await requests.load()
  assert.equal(get(requests)[0]?.state, 'withdrawn')
  assert.deepEqual(calls, ['', 'room_1'])
})
