import test from 'node:test'
import assert from 'node:assert/strict'
import vm from 'node:vm'
import { readFileSync } from 'node:fs'
import { stripTypes } from '../test/svelte-source.ts'

type Listener = (event: object) => void

interface Notification { title: string, options: Record<string, unknown> }
interface FakeClient { url: string, messages: object[], focused: boolean }

const ROOM = 'room_20260101-120000-abcdef01'
const ORIGIN = 'https://dyna.test'
const source = stripTypes(readFileSync(new URL('./sw.ts', import.meta.url), 'utf8'))

// Loads the worker source in a vm context with a fake service worker scope.
function load(clients: FakeClient[] = []) {
  const listeners = new Map<string, Listener>()
  const shown: Notification[] = []
  const opened: string[] = []
  class ServiceWorkerGlobalScope {
    location = { origin: ORIGIN }
    registration = { showNotification: (title: string, options: Record<string, unknown>) => { shown.push({ title, options }); return Promise.resolve() } }
    clients = {
      matchAll: () => Promise.resolve(clients),
      openWindow: (url: string) => { opened.push(url); return Promise.resolve() },
      claim: () => Promise.resolve(),
    }
    addEventListener(type: string, listener: Listener): void { listeners.set(type, listener) }
    skipWaiting(): Promise<void> { return Promise.resolve() }
  }
  const scope = new ServiceWorkerGlobalScope()
  vm.runInNewContext(source, { self: scope, ServiceWorkerGlobalScope, URL, Reflect })
  return { listeners, shown, opened }
}

// Dispatches an event and returns the promise handed to waitUntil.
async function dispatch(listeners: Map<string, Listener>, type: string, event: object): Promise<void> {
  let pending: Promise<unknown> = Promise.resolve()
  const listener = listeners.get(type)
  assert.ok(listener, `no ${type} listener`)
  listener({ ...event, waitUntil: (p: Promise<unknown>) => { pending = p } })
  await pending
}

// Objects made inside the vm context have another realm's prototypes, so compare them as JSON.
const plain = (value: unknown): unknown => JSON.parse(JSON.stringify(value))

const pushOf = (payload: unknown) => ({ data: { json: () => payload } })
const clickOf = (data: unknown) => ({ notification: { data, close: () => {} } })

test('push shows the title, body, tag and ids from the payload', async () => {
  const { listeners, shown } = load()
  await dispatch(listeners, 'push', pushOf({ t: 'turn', title: 'Room A', body: 'Hello', room: ROOM, post: 'p1' }))
  assert.deepEqual(plain(shown), [{ title: 'Room A', options: { body: 'Hello', tag: ROOM, requireInteraction: false, icon: '/icon-192.png', data: { room: ROOM, post: 'p1' } } }])
})

test('push falls back to defaults for missing or falsy fields and bad payloads', async () => {
  const { listeners, shown } = load()
  await dispatch(listeners, 'push', pushOf({ title: 0, body: '' }))
  await dispatch(listeners, 'push', pushOf('text'))
  await dispatch(listeners, 'push', { data: null })
  await dispatch(listeners, 'push', { data: { json: () => { throw new Error('bad json') } } })
  assert.equal(shown.length, 4)
  for (const n of shown) {
    assert.equal(n.title, 'Dyna')
    assert.deepEqual(plain(n.options), { body: 'New post in Dyna', tag: 'dyna', requireInteraction: false, icon: '/icon-192.png', data: {} })
  }
})

test('questions keep their own tag and failures share one tag per room', async () => {
  const { listeners, shown } = load()
  const push = (payload: object) => dispatch(listeners, 'push', pushOf(payload))
  await push({ t: 'turn', room: ROOM, post: 'p1', title: 'Ada finished a turn' })
  await push({ t: 'question', room: ROOM, post: 'p2', title: 'Ada asked a question' })
  await push({ t: 'question', room: ROOM, post: 'p3', title: 'Bea asked a question' })
  await push({ t: 'failure', room: ROOM, post: 'p4', title: 'Ada failed' })
  await push({ t: 'failure', room: ROOM, post: 'p5', title: 'Bea failed' })
  assert.deepEqual(plain(shown), [
    { title: 'Ada finished a turn', options: { body: 'New post in Dyna', tag: ROOM, requireInteraction: false, icon: '/icon-192.png', data: { room: ROOM, post: 'p1' } } },
    { title: 'Ada asked a question', options: { body: 'New post in Dyna', tag: 'question:p2', requireInteraction: true, icon: '/icon-192.png', data: { room: ROOM, post: 'p2' } } },
    { title: 'Bea asked a question', options: { body: 'New post in Dyna', tag: 'question:p3', requireInteraction: true, icon: '/icon-192.png', data: { room: ROOM, post: 'p3' } } },
    { title: 'Ada failed', options: { body: 'New post in Dyna', tag: 'failure:' + ROOM, requireInteraction: true, icon: '/icon-192.png', data: { room: ROOM, post: 'p4' } } },
    { title: 'Bea failed', options: { body: 'New post in Dyna', tag: 'failure:' + ROOM, requireInteraction: true, icon: '/icon-192.png', data: { room: ROOM, post: 'p5' } } },
  ])
})

test('a question without a post falls back to the room tag', async () => {
  const { listeners, shown } = load()
  await dispatch(listeners, 'push', pushOf({ t: 'question', room: ROOM }))
  assert.deepEqual(plain(shown), [
    { title: 'Dyna', options: { body: 'New post in Dyna', tag: 'question:' + ROOM, requireInteraction: true, icon: '/icon-192.png', data: { room: ROOM } } },
  ])
})

test('push with a null JSON payload throws and shows nothing, as before', async () => {
  const { listeners, shown } = load()
  await assert.rejects(dispatch(listeners, 'push', pushOf(null)), { name: 'TypeError' })
  assert.deepEqual(shown, [])
})

test('push converts other truthy field values to text, as before', async () => {
  const { listeners, shown } = load()
  await dispatch(listeners, 'push', pushOf({ title: 5, body: true, room: 7, post: 8 }))
  assert.deepEqual(plain(shown), [{ title: '5', options: { body: 'true', tag: '7', requireInteraction: false, icon: '/icon-192.png', data: { room: 7, post: 8 } } }])
})

test('a click with no open window opens a URL on the worker origin', async () => {
  const { listeners, opened } = load()
  await dispatch(listeners, 'notificationclick', clickOf({ room: ROOM, post: 'p_1-x' }))
  await dispatch(listeners, 'notificationclick', clickOf({ room: ROOM }))
  assert.deepEqual(opened, [`${ORIGIN}/#room=${ROOM}&post=p_1-x`, `${ORIGIN}/#room=${ROOM}`])
})

test('a click drops ids in the wrong shape', async () => {
  const { listeners, opened } = load()
  await dispatch(listeners, 'notificationclick', clickOf({ room: 'room_x', post: 'p1' }))
  await dispatch(listeners, 'notificationclick', clickOf({ room: ROOM, post: 'a/../b' }))
  await dispatch(listeners, 'notificationclick', clickOf({ room: [ROOM], post: ['p1'] }))
  await dispatch(listeners, 'notificationclick', clickOf(null))
  assert.deepEqual(opened, [`${ORIGIN}/`, `${ORIGIN}/#room=${ROOM}`, `${ORIGIN}/#room=${ROOM}&post=p1`, `${ORIGIN}/`])
})

test('a click on an open window posts array-valued ids unchanged', async () => {
  const ours: FakeClient = { url: `${ORIGIN}/`, messages: [], focused: false }
  const clients = [{ ...ours, postMessage: (m: object) => ours.messages.push(m), focus: () => Promise.resolve(ours) }]
  const { listeners } = load(clients)
  await dispatch(listeners, 'notificationclick', clickOf({ room: [ROOM], post: ['p1'] }))
  assert.deepEqual(plain(ours.messages), [{ type: 'open', room: [ROOM], post: ['p1'], call: '' }])
})

test('a click focuses a window on this origin and posts the target', async () => {
  const other: FakeClient = { url: 'https://evil.test/', messages: [], focused: false }
  const ours: FakeClient = { url: `${ORIGIN}/#room=old`, messages: [], focused: false }
  const clients = [other, ours].map((c) => ({ ...c, postMessage: (m: object) => c.messages.push(m), focus: () => { c.focused = true; return Promise.resolve(c) } }))
  const { listeners, opened } = load(clients)
  await dispatch(listeners, 'notificationclick', clickOf({ room: ROOM, post: 'p1' }))
  assert.deepEqual(opened, [])
  assert.deepEqual(other.messages, [])
  assert.equal(other.focused, false)
  assert.deepEqual(plain(ours.messages), [{ type: 'open', room: ROOM, post: 'p1', call: '' }])
  assert.equal(ours.focused, true)
})
